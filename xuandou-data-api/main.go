package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Twitter snowflake layout: 41-bit milliseconds, 10-bit node, 12-bit sequence.
const snowflakeEpoch int64 = 1704067200000 // 2024-01-01 UTC

func main() {
	loc, err := time.LoadLocation(env("TZ", "Asia/Shanghai"))
	if err != nil {
		loc = time.Local
	}
	disk := newDisk(env("DATA_DIR", "/data"), loc, workerID())

	httpsAddr := env("HTTPS_ADDR", "0.0.0.0:4000")
	httpAddr := env("HTTP_ADDR", "0.0.0.0:4080")
	tcpAddr := env("TCP_ADDR", "0.0.0.0:4001")
	udpAddr := env("UDP_ADDR", "0.0.0.0:4002")
	queryAddr := env("QUERY_ADDR", "0.0.0.0:3090")

	go func() {
		log.Printf("query listen on %s", queryAddr)
		log.Fatal(http.ListenAndServe(queryAddr, disk.queryHandler()))
	}()
	handler := disk.handler()
	go func() {
		log.Printf("http listen on %s", httpAddr)
		log.Fatal(http.ListenAndServe(httpAddr, handler))
	}()
	go listenTCP(tcpAddr, disk)
	go listenUDP(udpAddr, disk)

	certFile := env("TLS_CERT", "/certs/fullchain.pem")
	keyFile := env("TLS_KEY", "/certs/privkey.pem")
	if env("TLS_ENABLE", "true") != "true" {
		log.Printf("https disabled, data dir %s", disk.dir)
		select {}
	}
	log.Printf("https listen on %s, data dir %s", httpsAddr, disk.dir)
	log.Fatal(http.ListenAndServeTLS(httpsAddr, certFile, keyFile, handler))
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func workerID() uint64 {
	n, err := strconv.ParseUint(env("SNOWFLAKE_WORKER", "1"), 10, 10)
	if err != nil {
		return 1
	}
	return n & 0x3FF
}

type savedFile struct {
	ID       string `json:"id"`
	File     string `json:"file"`
	Size     int64  `json:"size"`
	Protocol string `json:"protocol"`
	Path     string `json:"path"`
}

type snowflake struct {
	mu   sync.Mutex
	node uint64
	seq  uint64
	last int64
}

func (s *snowflake) next(now time.Time) (string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := now.UnixMilli()
	if ms < s.last {
		ms = s.last
	}
	if ms == s.last {
		s.seq = (s.seq + 1) & 0xFFF
		if s.seq == 0 {
			ms++
		}
	} else {
		s.seq = 0
	}
	s.last = ms
	id := (uint64(ms-snowflakeEpoch) << 22) | (s.node << 12) | s.seq
	return strconv.FormatUint(id, 10), time.UnixMilli(ms).In(now.Location())
}

type disk struct {
	dir   string
	loc   *time.Location
	flake snowflake
}

func newDisk(dir string, loc *time.Location, node uint64) *disk {
	return &disk{dir: dir, loc: loc, flake: snowflake{node: node}}
}

func (d *disk) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			d.acceptWebSocket(w, r)
			return
		}
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if strings.HasPrefix(mediaType, "multipart/") {
			d.saveMultipart(w, r)
			return
		}
		item, err := d.write(scheme(r), r.URL.Path, r.Header.Get("Content-Type"), filenameFrom(r), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, item)
	})
}

func (d *disk) saveMultipart(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var items []savedFile
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := part.FileName()
		if name == "" {
			name = part.FormName()
		}
		item, err := d.write(scheme(r), r.URL.Path, part.Header.Get("Content-Type"), name, part)
		part.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, map[string]any{"files": items})
}

func (d *disk) write(protocol, reqPath, contentType, filename string, r io.Reader) (savedFile, error) {
	id, stamp := d.flake.next(time.Now().In(d.loc))
	ext := fileExt(contentType, filename)
	rel := filepath.ToSlash(filepath.Join(stamp.Format("2006"), stamp.Format("0102"), stamp.Format("150405")+"_"+id+"."+ext))
	full := filepath.Join(d.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return savedFile{}, err
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return savedFile{}, err
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return savedFile{}, copyErr
	}
	if closeErr != nil {
		return savedFile{}, closeErr
	}
	log.Printf("stored %s %s (%d bytes)", protocol, rel, n)
	return savedFile{ID: id, File: rel, Size: n, Protocol: protocol, Path: reqPath}, nil
}

func (d *disk) queryHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/messages") {
			http.NotFound(w, r)
			return
		}
		key := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/messages"), "/")
		items := d.list()
		if key == "" {
			writeJSON(w, map[string]any{"messages": items})
			return
		}
		var item *savedFile
		if key == "latest" {
			if len(items) == 0 {
				http.Error(w, "no messages", http.StatusNotFound)
				return
			}
			item = &items[0]
		} else {
			for i := range items {
				if items[i].ID == key {
					item = &items[i]
					break
				}
			}
		}
		if item == nil {
			http.Error(w, "message not found", http.StatusNotFound)
			return
		}
		full := filepath.Join(d.dir, filepath.FromSlash(item.File))
		f, err := os.Open(full)
		if err != nil {
			http.Error(w, "message not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		if ext := filepath.Ext(item.File); ext != "" {
			w.Header().Set("Content-Type", mime.TypeByExtension(ext))
		}
		_, _ = io.Copy(w, f)
	})
}

func (d *disk) list() []savedFile {
	var items []savedFile
	_ = filepath.WalkDir(d.dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		id := fileID(entry.Name())
		if id == "" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(d.dir, p)
		if err != nil {
			return nil
		}
		items = append(items, savedFile{
			ID:   id,
			File: filepath.ToSlash(rel),
			Size: info.Size(),
		})
		return nil
	})
	sort.Slice(items, func(i, j int) bool {
		a, _ := strconv.ParseUint(items[i].ID, 10, 64)
		b, _ := strconv.ParseUint(items[j].ID, 10, 64)
		return a > b
	})
	return items
}

func fileID(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	prefix, id, ok := strings.Cut(base, "_")
	if !ok || len(prefix) != 6 || !digits(prefix) || !snowflakeID(id) {
		return ""
	}
	return id
}

func digits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func snowflakeID(id string) bool {
	if id == "" || len(id) > 20 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func filenameFrom(r *http.Request) string {
	if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		return params["filename"]
	}
	base := path.Base(r.URL.Path)
	if strings.Contains(base, ".") && base != "." {
		return base
	}
	return ""
}

func fileExt(contentType, filename string) string {
	if ext := cleanExt(filepath.Ext(filename)); ext != "" {
		return ext
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		return "bin"
	}
	if ext, ok := extByType[mediaType]; ok {
		return ext
	}
	if strings.HasPrefix(mediaType, "text/") {
		return "txt"
	}
	if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
		if ext := cleanExt(exts[0]); ext != "" {
			return ext
		}
	}
	return "bin"
}

func cleanExt(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if ext == "" || len(ext) > 10 {
		return ""
	}
	for _, c := range ext {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return ""
		}
	}
	return ext
}

var extByType = map[string]string{
	"application/json":         "json",
	"application/pdf":          "pdf",
	"application/zip":          "zip",
	"application/xml":          "xml",
	"text/plain":               "txt",
	"text/html":                "html",
	"text/csv":                 "csv",
	"text/xml":                 "xml",
	"image/jpeg":               "jpg",
	"image/png":                "png",
	"image/gif":                "gif",
	"image/webp":               "webp",
	"audio/mpeg":               "mp3",
	"audio/wav":                "wav",
	"audio/mp4":                "m4a",
	"video/mp4":                "mp4",
	"video/webm":               "webm",
	"application/octet-stream": "bin",
}

func listenTCP(addr string, store *disk) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("tcp listen on %s", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("tcp accept: %v", err)
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			if _, err := store.write("tcp", c.RemoteAddr().String(), "", "", c); err != nil {
				log.Printf("tcp store: %v", err)
			}
		}(conn)
	}
}

func listenUDP(addr string, store *disk) {
	packet, err := net.ListenPacket("udp", addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("udp listen on %s", addr)
	buf := make([]byte, 64*1024)
	for {
		n, remote, err := packet.ReadFrom(buf)
		if err != nil {
			log.Printf("udp read: %v", err)
			continue
		}
		if _, err := store.write("udp", remote.String(), "", "", bytes.NewReader(append([]byte(nil), buf[:n]...))); err != nil {
			log.Printf("udp store: %v", err)
		}
	}
}

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func (d *disk) acceptWebSocket(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	hj, ok := w.(http.Hijacker)
	if !ok || key == "" {
		http.Error(w, "websocket upgrade failed", http.StatusBadRequest)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
	_ = buf.Flush()
	kind := "ws"
	if r.TLS != nil {
		kind = "wss"
	}
	for {
		opcode, payload, err := readWSFrame(conn)
		if err != nil {
			return
		}
		switch opcode {
		case 0x8:
			return
		case 0x9:
			_ = writeWSFrame(conn, 0xA, payload)
		case 0x1:
			ext := "txt"
			contentType := "text/plain"
			if json.Valid(payload) && len(payload) > 0 && (payload[0] == '{' || payload[0] == '[') {
				ext = "json"
				contentType = "application/json"
			}
			_, _ = d.write(kind, r.URL.Path, contentType, "frame."+ext, bytes.NewReader(payload))
		case 0x2:
			_, _ = d.write(kind, r.URL.Path, "application/octet-stream", "", bytes.NewReader(payload))
		}
	}
}

func readWSFrame(r io.Reader) (opcode byte, payload []byte, err error) {
	header := make([]byte, 2)
	if _, err = io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	opcode = header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err = io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err = io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext)
	}
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err = io.ReadFull(r, mask); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeWSFrame(w io.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode, byte(len(payload))}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
