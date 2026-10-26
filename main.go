package main

import (
	"io"
	"io/fs"
	"fmt"
	"strconv"
	"path/filepath"
	"log"
	"net/http"
	"os"
	"strings"
	"errors"
)

var STORAGE_PATH, CLIPBOARD_PATH string
var MAX_REQUEST_SIZE int64

func relayInitialize() {
	os.MkdirAll(STORAGE_PATH, 0o755)
	_, err := os.Stat(CLIPBOARD_PATH)
	if errors.Is(err, os.ErrNotExist) {
		file, err_ := os.Create(CLIPBOARD_PATH)
		if err_ != nil {
			log.Fatal("Couldn't create a clipboard file")
		}
		file.Close()
	} else if err != nil {
		log.Fatal("Couldn't get information on clipboard file")
	}
}

func getStorageDirectoryTree() (string, error) {
	result := strings.Builder{}

	err := filepath.WalkDir(STORAGE_PATH, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			relPath, err1 := filepath.Rel(STORAGE_PATH, path)
			if err1 != nil {
				return nil
			}
			result.WriteString(fmt.Sprintf("%s\n", relPath))
		}
		return nil
	})
	
	if err != nil {
		return "", fmt.Errorf("getStorageDirectoryTree: couldn't traverse storage: %w", err)
	}
	
	return result.String(), nil
}

func getTextFromClipboard() ([]byte, error) {
	contents, err := os.ReadFile(CLIPBOARD_PATH)
	if err != nil {
		return nil, fmt.Errorf("getTextFromClipboard: couldn't read the clipboard: %w", err)
	}
	return contents, nil
}

func getFileFromStorage(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("couldn't get the information about the file %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("couldn't get the file: %s is a directory", path)
	}
	return nil
}

func pasteTextToClipboard(newEntry []byte) error {
	file, err := os.OpenFile(CLIPBOARD_PATH, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("pasteTextToClipboard: couldn't open clipboard: %w", err)
	}
	defer file.Close()

	_, err = file.WriteString(fmt.Sprintf("\n%s\n", newEntry))
	if err != nil {
		return fmt.Errorf("pasteTextToClipboard: couldn't write to the clipboard: %w", err)
	}

	return nil
}

func saveFileToStorage(path string) (*os.File, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("couldn't create a new file %s: %w", path, err)
	}

	return file, nil
}

func sendTree(w http.ResponseWriter, r *http.Request) {
	logPrefix := log.Prefix()
	log.SetPrefix("[sendTree] ")
	defer log.SetPrefix(logPrefix)

	treeString, err := getStorageDirectoryTree()

	if err != nil {
		log.Printf("couldn't get storage directory tree: %v\n", err)
		http.Error(w, "Couldn't get storage directory tree", http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "%s", treeString)

	log.Printf("OK\n")
	return
}

func sendText(w http.ResponseWriter, r *http.Request) {
	logPrefix := log.Prefix()
	log.SetPrefix("[sendText] ")
	defer log.SetPrefix(logPrefix)
	
	text, err := getTextFromClipboard()

	if err != nil {
		log.Printf("couldn't send text: %v\n", err)
		http.Error(w, "Couldn't get the text from the clipboard", http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "%s", text)

	log.Printf("OK\n")
	return
}

func sendFile(w http.ResponseWriter, r *http.Request) {
	logPrefix := log.Prefix()
	log.SetPrefix("[sendFile] ")
	defer log.SetPrefix(logPrefix)

	path := fmt.Sprintf(".%s", r.URL.Path)
	err := getFileFromStorage(path)

	if err != nil {
		log.Printf("couldn't send file: %v\n", err)
		http.Error(w, "Couldn't get the file from the storage", http.StatusBadRequest)
		return
	}

	http.ServeFile(w, r, path)

	log.Printf("OK\n")
	return
}

func uploadText(w http.ResponseWriter, r *http.Request) {
	logPrefix := log.Prefix()
	log.SetPrefix("[uploadText] ")
	defer log.SetPrefix(logPrefix)

	newEntry, err := io.ReadAll(r.Body)

	if err != nil {
		log.Printf("couldn't read the body of the request: %w\n", err)
		http.Error(w, "Couldn't set the new clipboard entry", http.StatusInternalServerError)
		return
	}
	
	err = pasteTextToClipboard(newEntry)

	if err != nil {
		log.Printf("couldn't upload text: %v\n", err)
		http.Error(w, "Couldn't set the new clipboard entry", http.StatusInternalServerError)
		return
	}

	log.Printf("OK\n")
	return
}

func uploadFile(w http.ResponseWriter, r *http.Request) {
	logPrefix := log.Prefix()
	log.SetPrefix("[uploadFile] ")
	defer log.SetPrefix(logPrefix)

	path := fmt.Sprintf(".%s", r.URL.Path)
	file, err := saveFileToStorage(path)
	if err != nil {
		log.Printf("couldn't read file contents from the request's body: %v\n", err)
		http.Error(w, "Couldn't upload file", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	
	_, err = io.Copy(file, r.Body)
	if err != nil {
		log.Printf("couldn't save file to the storage: %v\n", err)
		http.Error(w, "Couldn't upload file", http.StatusInternalServerError)
		return
	}

	log.Printf("OK\n")
}

type RestrictingMiddleware struct {
	next http.Handler
	maxRequestSize int64
}

func (m *RestrictingMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, m.maxRequestSize)
	m.next.ServeHTTP(w, r)
}

func main() {
	STORAGE_PATH = os.Getenv("STORAGE_PATH")
	CLIPBOARD_PATH = os.Getenv("CLIPBOARD_PATH")
	MAX_REQUEST_SIZE, convErr := strconv.ParseInt(os.Getenv("MAX_REQUEST_SIZE"), 10, 64)
	if convErr != nil {
		MAX_REQUEST_SIZE = 100 * 1024 * 1024
	}
	
	relayInitialize()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /home", sendTree)
	mux.HandleFunc("GET /storage/", sendFile)
	mux.HandleFunc("GET /clipboard", sendText)
	mux.HandleFunc("POST /storage/", uploadFile)
	mux.HandleFunc("POST /clipboard", uploadText)

	mw := &RestrictingMiddleware{next: mux, maxRequestSize: MAX_REQUEST_SIZE}
	
	relayServer := http.Server{
		Addr: ":8080",
		Handler: mw,
	}

	log.Printf("Relay server is working\n")
	err := relayServer.ListenAndServe()
	if err != nil {
		log.Fatal(err)
	}
}
