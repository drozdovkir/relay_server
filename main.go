package main

import (
	"io"
	"io/fs"
	"fmt"
	"strconv"
	"encoding/binary"
	"path/filepath"
	"log"
	"net/http"
	"os"
	"strings"
)

var (
	STORAGE_PATH, CLIPBOARD_PATH, CLIPBOARD_INDEX_PATH string
	MAX_REQUEST_SIZE int64
	
	index []int64

	clipboardSize int64
)

func checkCreateFile(path string) (*os.File, error) {
	var file *os.File
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("Error while handling %s: %w", path, err)
	}
	return file, nil
}

func relayInitialize() {
	os.MkdirAll(STORAGE_PATH, 0o755)
	
	clipboardFile, err := checkCreateFile(CLIPBOARD_PATH)
	if err != nil {
		log.Fatal("Initialization error: %v", err)
	}
	defer clipboardFile.Close()
	
	clipboardIndexFile, err := checkCreateFile(CLIPBOARD_INDEX_PATH)
	if err != nil {
		log.Fatal("Initialization error: %v", err)
	}
	defer clipboardIndexFile.Close()

	info, _ := clipboardFile.Stat()
	clipboardSize = info.Size()
	log.Printf("clipboard size %d\n", clipboardSize)

	info, _ = clipboardIndexFile.Stat()
	clipboardIndexSize := info.Size()
	log.Printf("clipboard index size %d\n", clipboardIndexSize)
	
	index = make([]int64, clipboardIndexSize/8)
	for k := 0; k < len(index); k++ {
		err = binary.Read(clipboardIndexFile, binary.BigEndian, &index[k])
		if err != nil {
			break
		}
	}

	if err != nil && err != io.EOF {
		log.Fatal("Invalid format of clipboard index")
	}

	for len(index) - 1 >= 0 && index[len(index) - 1] >= clipboardSize {
		index = index[:len(index)-1]
		clipboardIndexFile.Truncate((int64)(8*len(index)))
		log.Printf("Invalid clipboard index entry removed\n")
	}

	log.Printf("Relay server: initialization complete\n")
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

func getTextFromClipboard(n int) ([]byte, error) {
	// Emulating Python's negative indexing
	if (n >= len(index)) || (-n > len(index)) {
		return nil, fmt.Errorf("getTextFromClipboard: index out of bounds")
	}
	
	file, err := os.Open(CLIPBOARD_PATH)
	if err != nil {
		return nil, fmt.Errorf("getTextFromClipboard: couldn't open the clipboard: %w", err)
	}
	defer file.Close()
	
	n_ := n
	if n_ < 0 {
		n_ = len(index) + n_
	}
	begin := index[n_]
	
	end := clipboardSize
	if n_ + 1 < len(index) {
		end = index[n_ + 1]
	}
	
	buffer := make([]byte, end - begin)
	file.Seek((int64)(begin), io.SeekStart)
	_, err = file.Read(buffer)
	
	if err != nil {
		return nil, fmt.Errorf("getTextFromClipboard: couldn't read the clipboard: %w", err)
	}
	
	return buffer, nil
}

func checkFileInStorage(path string) error {
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
	clipboardFile, err := os.OpenFile(CLIPBOARD_PATH, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("pasteTextToClipboard: couldn't open clipboard: %w", err)
	}
	defer clipboardFile.Close()

	clipboardIndexFile, err := os.OpenFile(CLIPBOARD_INDEX_PATH, os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("pasteTextToClipboard: couldn't open clipboard index: %w", err)
	}
	defer clipboardIndexFile.Close()

	clipboardIndexFile.Seek((int64)(8*len(index)), io.SeekStart)
	
	err = binary.Write(clipboardIndexFile, binary.BigEndian, clipboardSize)
	if err != nil {
		return fmt.Errorf("pasteTextToClipboard: couldn't write to the clipboard index: %w", err)
	}
	
	_, err = clipboardFile.WriteString(fmt.Sprintf("%s", newEntry))
	if err != nil {
		clipboardIndexFile.Truncate(int64(8*len(index)))
		return fmt.Errorf("pasteTextToClipboard: couldn't write to the clipboard: %w", err)
	}
	
	index = append(index, clipboardSize)
	clipboardSize += (int64)(len(newEntry))
	return nil
}

func saveFileToStorage(path string, requestBody io.Reader) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("couldn't create a new file %s: %w", path, err)
	}
	defer file.Close()
	
	_, err = io.Copy(file, requestBody)
	if err != nil {
		return fmt.Errorf("couldn't copy the contents of the body to file: %w", err)
	}
	
	return nil
}

func getTree(w http.ResponseWriter, r *http.Request) {
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

func getText(w http.ResponseWriter, r *http.Request) {
	logPrefix := log.Prefix()
	log.SetPrefix("[sendText] ")
	defer log.SetPrefix(logPrefix)

	n, convErr := strconv.ParseInt(r.URL.Query().Get("n"), 10, 0)
	if convErr != nil {
		log.Printf("couldn't parse the request's URL params: %v\n", convErr)
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	
	text, err := getTextFromClipboard((int)(n))

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

func getFile(w http.ResponseWriter, r *http.Request) {
	logPrefix := log.Prefix()
	log.SetPrefix("[sendFile] ")
	defer log.SetPrefix(logPrefix)

	path := fmt.Sprintf(".%s", r.URL.Path)
	err := checkFileInStorage(path)

	if err != nil {
		log.Printf("couldn't send file: %v\n", err)
		http.Error(w, "Couldn't get the file from the storage", http.StatusBadRequest)
		return
	}

	http.ServeFile(w, r, path)

	log.Printf("OK\n")
	return
}

func sendText(w http.ResponseWriter, r *http.Request) {
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

func sendFile(w http.ResponseWriter, r *http.Request) {
	logPrefix := log.Prefix()
	log.SetPrefix("[uploadFile] ")
	defer log.SetPrefix(logPrefix)

	path := fmt.Sprintf(".%s", r.URL.Path)
	err := saveFileToStorage(path, r.Body)
	if err != nil {
		log.Printf("couldn't upload file: %v\n", err)
		http.Error(w, "Couldn't upload file", http.StatusInternalServerError)
		return
	}
	
	log.Printf("OK\n")
}

type SimpleLogger struct {
	
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
	CLIPBOARD_INDEX_PATH = os.Getenv("CLIPBOARD_INDEX_PATH")
	MAX_REQUEST_SIZE, convErr := strconv.ParseInt(os.Getenv("MAX_REQUEST_SIZE"), 10, 64)
	if convErr != nil {
		MAX_REQUEST_SIZE = 100 * 1024 * 1024
	}
	
	relayInitialize()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /home", getTree)
	mux.HandleFunc("GET /storage/", getFile)
	mux.HandleFunc("GET /clipboard", getText)
	mux.HandleFunc("POST /storage/", sendFile)
	mux.HandleFunc("POST /clipboard", sendText)

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
