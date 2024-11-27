package service

import (
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/jackdallas/premiumizearr/internal/config"
	"github.com/jackdallas/premiumizearr/internal/directory_watcher"
	"github.com/jackdallas/premiumizearr/internal/utils"
	"github.com/jackdallas/premiumizearr/pkg/premiumizeme"
	"github.com/jackdallas/premiumizearr/pkg/stringqueue"
	log "github.com/sirupsen/logrus"
)

type DirectoryWatcherService struct {
	premiumizemeClient *premiumizeme.Premiumizeme
	config             *config.Config
	Queue              *stringqueue.StringQueue
	status             string
	downloadsFolderID  string
	watchDirectory     *directory_watcher.WatchDirectory
}

const (
	ERROR_LIMIT_REACHED    = "Limit of transfers reached!"
	ERROR_ALREADY_UPLOADED = "You already added this job."
)

func (DirectoryWatcherService) New() DirectoryWatcherService {
	return DirectoryWatcherService{
		premiumizemeClient: nil,
		config:             nil,
		Queue:              nil,
		status:             "",
		downloadsFolderID:  "",
	}
}

func (dw *DirectoryWatcherService) Init(premiumizemeClient *premiumizeme.Premiumizeme, config *config.Config) {
	dw.premiumizemeClient = premiumizemeClient
	dw.config = config
}

func (dw *DirectoryWatcherService) ConfigUpdatedCallback(currentConfig config.Config, newConfig config.Config) {
	if currentConfig.BlackholeDirectory != newConfig.BlackholeDirectory {
		log.Info("DirectoryWatcherService: Blackhole directory changed, restarting directory watcher...")
		log.Info("DirectoryWatcherService: Running initial directory scan...")
		go dw.directoryScan(dw.config.BlackholeDirectory)
		dw.watchDirectory.UpdatePath(newConfig.BlackholeDirectory)
	}

	if currentConfig.PollBlackholeDirectory != newConfig.PollBlackholeDirectory {
		log.Info("DirectoryWatcherService: Poll blackhole directory changed, restarting directory watcher...")
		dw.Start()
	}
}

func (dw *DirectoryWatcherService) GetStatus() string {
	return dw.status
}

//Start: This is the entrypoint for the directory watcher
func (dw *DirectoryWatcherService) Start() {
	log.Info("DirectoryWatcherService: Starting directory watcher...")

	dw.downloadsFolderID = utils.GetDownloadsFolderIDFromPremiumizeme(dw.premiumizemeClient, dw.config.PremiumizemeFolderName)

	log.Info("DirectoryWatcherService: Creating Queue...")
	dw.Queue = stringqueue.NewStringQueue()

	log.Info("DirectoryWatcherService: Starting uploads processor...")
	go dw.processUploads()

	log.Info("DirectoryWatcherService: Running initial directory scan...")
	go dw.directoryScan(dw.config.BlackholeDirectory)

	if dw.watchDirectory != nil {
		log.Info("DirectoryWatcherService: Stopping directory watcher...")
		err := dw.watchDirectory.Stop()
		if err != nil {
			log.Errorf("DirectoryWatcherService: Error stopping directory watcher: %s", err)
		}
	}

	if dw.config.PollBlackholeDirectory {
		log.Info("DirectoryWatcherService: Starting directory poller...")
		go func() {
			for {
				if !dw.config.PollBlackholeDirectory {
					log.Info("DirectoryWatcherService: Directory poller stopped")
					break
				}
				time.Sleep(time.Duration(dw.config.PollBlackholeIntervalMinutes) * time.Minute)
				log.Infof("DirectoryWatcherService: Running directory scan of %s", dw.config.BlackholeDirectory)
				dw.directoryScan(dw.config.BlackholeDirectory)
				log.Infof("DirectoryWatcherService: Scan complete, next scan in %d minutes", dw.config.PollBlackholeIntervalMinutes)
			}
		}()
	} else {
		log.Info("DirectoryWatcherService: Starting directory watcher...")
		dw.watchDirectory = directory_watcher.NewDirectoryWatcher(dw.config.BlackholeDirectory,
			false,
			dw.checkFile,
			dw.addFileToQueue,
		)
		dw.watchDirectory.Watch()
	}
}

func (dw *DirectoryWatcherService) directoryScan(p string) {
	log.Trace("DirectoryWatcherService: Running directory scan")
	files, err := ioutil.ReadDir(p)
	if err != nil {
		log.Errorf("DirectoryWatcherService: Error with directory scan %+v", err)
		return
	}

	for _, file := range files {
		go func(file os.FileInfo) {
			file_path := path.Join(p, file.Name())
			if dw.checkFile(file_path) {
				dw.addFileToQueue(file_path)
			}
		}(file)
	}
}

func (dw *DirectoryWatcherService) checkFile(path string) bool {
	log.Tracef("DirectoryWatcherService: Checking file %s", path)

	fi, err := os.Stat(path)
	if err != nil {
		log.Errorf("DirectoryWatcherService: Error checking file %s", path)
		return false
	}

	if fi.IsDir() {
		log.Errorf("DirectoryWatcherService: Directory created in blackhole %s ignoring (Warning premiumizearrd does not look in subfolders!)", path)
		return false
	}

	ext := filepath.Ext(path)
	if ext == ".nzb" || ext == ".magnet" {
		return true
	} else {
		return false
	}
}

func (dw *DirectoryWatcherService) addFileToQueue(path string) {
	dw.Queue.Add(path)
	log.Infof("DirectoryWatcherService: File created in blackhole %s added to Queue. Queue length %d", path, dw.Queue.Len())
}

func (dw *DirectoryWatcherService) processUploads() {
	for {
		if dw.Queue.Len() < 1 {
			log.Trace("DirectoryWatcherService: No files in Queue, sleeping for 10 seconds")
			time.Sleep(time.Second * time.Duration(10))
		}

		isQueueFile, filePath := dw.Queue.PopTopOfQueue()
		if !isQueueFile {
			time.Sleep(time.Second * time.Duration(10))
			continue
		}

		sleepTimeSeconds := 2
		if filePath != "" {
			log.Debugf("DirectoryWatcherService: Processing %s", filePath)
			err := dw.premiumizemeClient.CreateTransfer(filePath, dw.downloadsFolderID)
			if err != nil {
				switch err.Error() {
				case ERROR_LIMIT_REACHED:
					dw.status = "Limit of transfers reached!"
					log.Trace("DirectoryWatcherService: Transfer limit reached waiting 10 seconds and retrying")
					sleepTimeSeconds = 10
				case ERROR_ALREADY_UPLOADED:
					log.Trace("DirectoryWatcherService: File already uploaded, removing from Disk")
					os.Remove(filePath)
				default:
					log.Errorf("DirectoryWatcherService: Error creating transfer: %s", err)
				}
			} else {
				dw.status = "Okay"
				err := os.Remove(filePath)
				if err != nil {
					log.Errorf("DirectoryWatcherService: Error could not delete %s Error: %+v", filePath, err)
				}
				log.Infof("DirectoryWatcherService: Removed %s from blackhole Queue. Queue Size: %d", filePath, dw.Queue.Len())
			}
			time.Sleep(time.Second * time.Duration(sleepTimeSeconds))
		} else {
			log.Errorf("DirectoryWatcherService: Received filePath from blackhole Queue appears to be an empty path.")
		}
	}
}
