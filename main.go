package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/net/webdav"
)

type Users struct {
	Users []User `json:"Users"`
}

type User struct {
	Name     string `json:"name"`
	Password string `json:"password"` // SHA256で暗号化保存すること
}

type File struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Extension string `json:"extension"`
	IsDir     bool   `json:"isDir"`
	Date      string `json:"date"`
	Size      int64  `json:"size"`
}

var (
	// WebDav Config
	fileDirectory = flag.String("dir", "./files", "File Directory")
	configs       = flag.String("config", "./config", "Config Files Directory")
	httpPort      = flag.Int("http", 80, "HTTP Request Port")
	httpsPort     = flag.Int("https", 443, "HTTPS Request Port")
	ssl           = flag.Bool("ssl", false, "Listen HTTPS Request")
	enableBasic   = flag.Bool("basic", false, "Enable Basic Auth(Access to \"dir/Username/\"")
	enableShare   = flag.Bool("share", false, "Enable Share Directory(Access to \"dir/\"")
	// WebDav Config
	webdavHandler *webdav.Handler
	// おまけ
	password        = flag.String("pass", "", "Password to SHA256")
	maxMemory int64 = *flag.Int64("ram", 512000000, "Post Max")
)

func main() {
	// Flag Parse and View
	flag.Parse()
	if *password != "" {
		fmt.Printf("%s => %x", *password, sha256.Sum256([]byte(*password)))
		return
	}
	if *enableShare && !*enableBasic {
		*enableShare = false
	}
	fmt.Printf("WebDav Boot Config\n")
	fmt.Printf("File Directory         : %s\n", *fileDirectory)
	fmt.Printf("Config Files Directory : %s\n", *configs)
	fmt.Printf("HTTP Port              : %d\n", *httpPort)
	fmt.Printf("HTTPS Port             : %d\n", *httpsPort)
	fmt.Printf("Secure(SSL)            : %t\n", *ssl)
	fmt.Printf("Basic Authentication   : %t #HTTPSでない場合は不安定です。\n", *enableBasic)
	fmt.Printf("Share User Directory   : %t #Required: Basic Auth\n", *enableShare)

	// Check Basic
	if *enableBasic {
		_, err := os.Stat(filepath.Join(*configs, "users.json"))
		if err != nil {
			log.Fatalf("Failed WebDav Server Boot Prerequisite file(%s): %v", filepath.Join(*configs, "users.json"), err)
		}
	}

	// Webdav Init
	webdavHandler = &webdav.Handler{
		FileSystem: webdav.Dir(*fileDirectory),
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			log.Printf("IP:%s \"%s\" %s, ERR: %v\n", r.RemoteAddr, r.Method, r.URL, err)
		},
	}
	// HTTP, HTTPS server
	http.HandleFunc("/", HttpRequest)
	if *ssl {
		var isHttpsBoot = true
		_, err := os.Stat(filepath.Join(*configs, "cert.pem"))
		if err != nil {
			log.Printf("Failed WebDav Server Boot Prerequisite file(%s): %v", filepath.Join(*configs, "cert.pem"), err)
			isHttpsBoot = false
		}
		_, err = os.Stat(filepath.Join(*configs, "key.pem"))
		if err != nil {
			log.Printf("Failed WebDav Server Boot Prerequisite file(%s): %v", filepath.Join(*configs, "key.pem"), err)
			isHttpsBoot = false
		}

		if isHttpsBoot {
			go http.ListenAndServeTLS(fmt.Sprintf(":%d", *httpsPort), "cert.pem", "key.pem", nil)
			log.Println("HTTP WebDav Server has Boot!")
		} else {
			log.Println("Skip HTTPS WebDav Server Boot")
		}
	}
	go func() {
		err := http.ListenAndServe(fmt.Sprintf(":%d", *httpPort), nil)
		if err != nil {
			log.Fatalf("Failed WebDav Server boot: %v", err)
		}
	}()
	log.Println("HTTP WebDav Server has Boot!")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

func HttpRequest(w http.ResponseWriter, r *http.Request) {
	name := ""
	// Basic Auth
	if *enableBasic {
		w.Header().Set("WWW-Authenticate", `Basic realm="Check Login User"`)
		username, password, authOK := r.BasicAuth()

		if !authOK || username == name {
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}

		// User List
		jsonData, err := os.ReadFile(filepath.Join(*configs, "users.json"))
		if err != nil {
			log.Printf("Failed Basic Authorized(read file): %v", err)
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}
		var config Users
		err = json.Unmarshal(jsonData, &config)
		if err != nil {
			log.Printf("Failed Basic Authorized(json unmarshal): %v", err)
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}

		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(password)))
		log.Printf("IP:%s \"LOGIN\" %s:%s\n", r.RemoteAddr, username, hash)

		// Check Auth
		var isAuthSuccess = false
		for _, user := range config.Users {
			if username == user.Name && hash == user.Password {
				isAuthSuccess = true
				break
			}
		}
		if !isAuthSuccess {
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}

		if !*enableShare {
			// create dir
			parent := filepath.Join(*fileDirectory, username)
			_, err = os.Stat(parent)
			if err != nil {
				err := os.Mkdir(parent, 0777)
				if err != nil {
					log.Printf("Failed Create Dir(%s): %v", parent, err)
					http.Error(w, "Failed Create User Dir", http.StatusUnauthorized)
					return
				}
			}
			name = username
		}
	}

	if r.Header.Get("Translate") != "f" { // Browser Check?
		switch r.Method {
		case http.MethodGet:
			path := filepath.Join(*fileDirectory, name, r.URL.Path)
			if *enableBasic {
				// Check Request File
				requestFile, err := os.Stat(path)
				if err != nil {
					log.Printf("Failed Read Directory/File(%s): %v", path, err)
					http.Error(w, "Failed Read Dir/File", http.StatusNotFound)
					return
				}

				// Read Directory
				if requestFile.IsDir() {
					ReadDirectory(w, r, path)
					return
				}
				// Not Directory
				DownloadFile(w, r, path)
			} else {
				// Check Request File
				requestFile, err := os.Stat(path)
				if err != nil {
					passwords := r.URL.Query()["pass"]
					if len(passwords) != 1 {
						log.Printf("Failed Access Directory/File(%s): %v", path, err)
						http.Error(w, "Failed Access Dir/File", http.StatusNotFound)
					}
					path = fmt.Sprintf("%s__%s", path, passwords[0])
					DownloadFile(w, r, path)
					return
				}

				// Read Directory
				if requestFile.IsDir() {
					ReadDirectory(w, r, path)
					return
				}
				// Not Directory
				log.Printf("Failed Access Directory/File(%s): %v", path, err)
				http.Error(w, "Failed Access Dir/File", http.StatusNotFound)
				return
			}

		case http.MethodPost:
			r.ParseMultipartForm(maxMemory)
			formItems := r.MultipartForm.File["file"]
			for i, item := range formItems {
				src, err := item.Open()
				if err != nil {
					log.Printf("Failed Read UploadFile: %+v", item)
					http.Error(w, "Failed Read UploadFile", http.StatusNoContent)
					return
				}
				defer src.Close()

				savePath := filepath.Join(*fileDirectory, name, r.URL.Path, item.Filename)
				for i := 1; true; i++ {
					_, err := os.Stat(savePath)
					if err != nil {
						break
					}
					savePath = filepath.Join(*fileDirectory, name, r.URL.Path, fmt.Sprintf("%s-%d%s", filepath.Base(item.Filename[:len(item.Filename)-len(filepath.Ext(item.Filename))]), i, filepath.Ext(item.Filename)))
				}
				if !*enableBasic {
					savePath = fmt.Sprintf("%s__%s", savePath, r.MultipartForm.Value["pass"][i])
				}
				dst, err := os.Create(savePath)
				if err != nil {
					log.Printf("Failed Save UploadFile: %v", item)
					http.Error(w, "Failed Save UploadFile", http.StatusNoContent)
					return
				}
				defer dst.Close()

				io.Copy(dst, src)
				log.Println("Upload File is Saved.", savePath)
			}
			w.WriteHeader(200)
			return

		default:
			log.Println("Unknown Method", r.Method)
			r.URL.Path = filepath.Join(name, r.URL.Path) // 念のため
		}
	} else {
		r.URL.Path = filepath.Join(name, r.URL.Path)
	}

	if *enableBasic {
		webdavHandler.ServeHTTP(w, r)
	}
}

func DownloadFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.ReadFile(path)
	if err != nil {
		log.Printf("Failed Read File: %v", err)
		http.Error(w, "Failed Read Dir/File", http.StatusNotFound)
		return
	}
	w.Header().Add("Content-Type", "application/force-download")
	w.Header().Add("Content-Length", fmt.Sprintf("%d", len(f)))
	w.Header().Add("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(r.URL.Path)))
	w.WriteHeader(200)
	w.Write(f)
}

func ReadDirectory(w http.ResponseWriter, r *http.Request, path string) {
	files, err := os.ReadDir(path)
	if err != nil {
		log.Printf("Failed Read Directory(%s): %v", path, err)
		http.Error(w, "Failed Read Dir/File", http.StatusNotFound)
		return
	}
	var directoryFiles []File
	// Root
	directoryFiles = append(directoryFiles, File{
		Name:      "/",
		Path:      "/",
		Extension: "Directory",
		IsDir:     true,
	})
	// Parent
	directoryFiles = append(directoryFiles, File{
		Name:      "../",
		Path:      "../",
		Extension: "Directory",
		IsDir:     true,
	})
	// Directory Files
	for _, f := range files {
		fileStatus, _ := os.Stat(filepath.Join(path, f.Name()))
		fileName := f.Name()
		if !*enableBasic {
			names := strings.Split(f.Name(), "__")
			fileName = strings.Join(names[:len(names)-1], "__")
		}

		fileInfo := File{
			Name:      fileName,
			Path:      filepath.Join(r.URL.Path, fileName),
			Extension: filepath.Ext(fileName),
			IsDir:     fileStatus.IsDir(),
			Date:      fileStatus.ModTime().Format("2006/01/02-15:04:05"),
			Size:      fileStatus.Size(),
		}
		if f.IsDir() {
			fileInfo.Name += "/"
			fileInfo.Extension = "Directory"
		}

		directoryFiles = append(directoryFiles, fileInfo)
	}

	// Result File Create
	temp, err := os.ReadFile(filepath.Join(*configs, "template.html"))
	if err != nil {
		log.Printf("Failed Read File(%s): %v", filepath.Join(*configs, "template.html"), err)
		http.Error(w, "Failed Read Dir/File", http.StatusNotFound)
		return
	}
	indexFile := string(temp)
	directoryFilesBytes, _ := json.Marshal(directoryFiles)
	indexFile = strings.Replace(indexFile, "${files}", string(directoryFilesBytes), 1)
	if *enableBasic {
		indexFile = strings.Replace(indexFile, "${files}", "disable", 1)
	} else {
		indexFile = strings.Replace(indexFile, "${files}", "", 1)
	}
	// Return
	w.Write([]byte(indexFile))
}
