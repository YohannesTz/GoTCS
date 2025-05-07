package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	URLTemplate string            `json:"url_template"`
	OutputDir   string            `json:"output_dir"`
	Headers     map[string]string `json:"headers"`
	BBox        [4]float64        `json:"bbox"` // [minLon, minLat, maxLon, maxLat]
	MinZoom     int               `json:"min_zoom"`
	MaxZoom     int               `json:"max_zoom"`
}

type TileTask struct {
	Z, X, Y int
	URL     string
}

// --- Configuration via Flags ---
var (
	// Web UI / General
	webUIPort = flag.Int("web-ui-port", 3500, "Port to serve the web UI on (if no other mode is specified).")
	workers   = flag.Int("workers", runtime.NumCPU()*2, "Number of worker goroutines for downloading tiles.")

	// Download Mode
	urlTemplate   = flag.String("url-template", "", "URL template for tiles (e.g., 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png'). Required for download mode.")
	outputDir     = flag.String("output-dir", "", "Directory to save downloaded tiles. Required for download mode.")
	bboxString    = flag.String("bbox", "", "Bounding box 'minLon,minLat,maxLon,maxLat'. Required for download mode.")
	minZoom       = flag.Int("min-zoom", -1, "Minimum zoom level. Required for download mode.")
	maxZoom       = flag.Int("max-zoom", -1, "Maximum zoom level. Required for download mode.")
	headersString = flag.String("headers", "{}", "JSON string of headers to add to tile requests (e.g., '{\"User-Agent\":\"MyCacher\"}').")
	proxyFile     = flag.String("proxy-file", "", "Path to a file containing a list of proxies (one per line, e.g., http://user:pass@host:port). Proxies are rotated per worker.")

	// Tile Serving Mode
	serveTilesDir       = flag.String("serve-tiles", "", "Directory containing tiles (z/x/y.png structure) to serve.")
	tileServerPort      = flag.Int("tile-server-port", 8080, "Port to serve tiles on.")
	originURLTemplate   = flag.String("origin-url-template", "", "[Serving Mode] Original tile URL template to fetch tiles if missing locally.")
	originHeadersString = flag.String("origin-headers", "{}", "[Serving Mode] JSON string of headers for fetching missing tiles from origin.")
)

// Global HTTP client for fetching missing tiles in server mode
var originFetchClient *http.Client
var originHeaders map[string]string // Global for server mode header reuse

func main() {
	rand.Seed(time.Now().UnixNano())
	setupUsage() // Setup custom usage message
	flag.Parse()

	// --- Mode Determination ---

	// 1. Tile Serving Mode
	if *serveTilesDir != "" {
		runTileServer(*serveTilesDir, *tileServerPort, *originURLTemplate, *originHeadersString)
		return // Exit after starting server
	}

	// 2. Headless Download Mode
	if *urlTemplate != "" && *outputDir != "" && *bboxString != "" && *minZoom != -1 && *maxZoom != -1 {
		runHeadlessDownload()
		return // Exit after download
	}

	// 3. Web UI Mode (Default)
	runWebUI(*webUIPort, *workers)
}

// --- Mode Execution Functions ---

func runWebUI(port, numWorkers int) {
	log.Println("🚀 Starting Web UI mode.")
	addr := fmt.Sprintf(":%d", port)

	portProvided := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "web-ui-port" {
			portProvided = true
		}
	})
	if !portProvided {
		log.Printf("ℹ️ No --web-ui-port provided, serving UI on default port %d.", port)
	}

	http.Handle("/", http.FileServer(http.Dir("./ui")))
	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		handleStartDownloadRequest(w, r, numWorkers)
	})

	log.Printf("🌍 Web UI available at http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func runHeadlessDownload() {
	log.Println("🚀 Starting Headless Download mode.")

	bbox, err := parseBBox(*bboxString)
	if err != nil {
		log.Fatalf("❌ Invalid --bbox format: %v. Expected 'minLon,minLat,maxLon,maxLat'.", err)
	}

	headers, err := parseHeaders(*headersString)
	if err != nil {
		log.Fatalf("❌ Invalid --headers JSON: %v.", err)
	}

	proxies, err := loadProxies(*proxyFile)
	if err != nil {
		log.Fatalf("❌ Error loading proxies from %s: %v", *proxyFile, err)
	}
	if len(proxies) > 0 {
		log.Printf(" Caching using %d proxies from %s", len(proxies), *proxyFile)
	}

	cfg := Config{
		URLTemplate: *urlTemplate,
		OutputDir:   *outputDir,
		Headers:     headers,
		BBox:        bbox,
		MinZoom:     *minZoom,
		MaxZoom:     *maxZoom,
	}

	if cfg.MinZoom > cfg.MaxZoom {
		log.Fatalf("❌ Min zoom (%d) cannot be greater than max zoom (%d).", cfg.MinZoom, cfg.MaxZoom)
	}
	if cfg.MinZoom < 0 || cfg.MaxZoom < 0 {
		log.Fatalf("❌ Zoom levels must be non-negative.")
	}

	downloadTiles(cfg, *workers, proxies)
}

func runTileServer(tileDir string, port int, originURL, originHeadersJSON string) {
	log.Printf("🚀 Starting Tile Server mode. Serving tiles from: %s", tileDir)
	addr := fmt.Sprintf(":%d", port)

	if _, err := os.Stat(tileDir); os.IsNotExist(err) {
		log.Printf("⚠️ Tile directory '%s' does not exist. Will create if needed by origin fetching.", tileDir)
		// Don't fatal yet, maybe we only fetch from origin
	}

	// Setup client and headers for origin fetching if enabled
	if originURL != "" {
		log.Printf("📡 Origin fetch enabled: %s", originURL)
		originFetchClient = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				// Allow potentially self-signed certs on origin if needed, remove if not
				// TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				Proxy: http.ProxyFromEnvironment, // Use system proxy settings for origin fetches
			},
		}
		var err error
		originHeaders, err = parseHeaders(originHeadersJSON)
		if err != nil {
			log.Fatalf("❌ Invalid --origin-headers JSON: %v", err)
		}
		log.Printf("🏷️ Using %d custom headers for origin fetch.", len(originHeaders))
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		serveTileHandler(w, r, tileDir, originURL)
	})

	log.Printf("📡 Tile server listening on http://localhost%s/{z}/{x}/{y}.png", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// --- Helper Functions ---

func setupUsage() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Tile Cacher & Server\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  %s [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Modes:\n")
		fmt.Fprintf(os.Stderr, "  1. Web UI (Default): Run without download or serve flags.\n")
		fmt.Fprintf(os.Stderr, "     Example: %s\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  2. Headless Download: Provide all download flags.\n")
		fmt.Fprintf(os.Stderr, "     Example: %s --url-template='...' --output-dir=./tiles --bbox='...' --min-zoom=10 --max-zoom=15 [--proxy-file=proxies.txt]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  3. Tile Server: Serve previously downloaded tiles, optionally fetching missing ones.\n")
		fmt.Fprintf(os.Stderr, "     Example: %s --serve-tiles=./tiles [--origin-url-template='...' --origin-headers='{\"Key\":\"Val\"}']\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
}

func parseBBox(bboxStr string) ([4]float64, error) {
	var bbox [4]float64
	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		return bbox, fmt.Errorf("incorrect number of values, expected 4, got %d", len(parts))
	}
	for i, s := range parts {
		val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return bbox, fmt.Errorf("error parsing value '%s': %w", s, err)
		}
		bbox[i] = val
	}
	if bbox[0] >= bbox[2] {
		log.Printf("⚠️ Warning: minLon (%f) >= maxLon (%f) in bbox.", bbox[0], bbox[2])
	}
	if bbox[1] >= bbox[3] {
		return bbox, fmt.Errorf("minLat (%f) must be less than maxLat (%f)", bbox[1], bbox[3])
	}
	return bbox, nil
}

func parseHeaders(headersJson string) (map[string]string, error) {
	var headers map[string]string
	if headersJson == "" || headersJson == "{}" {
		return make(map[string]string), nil
	}
	err := json.Unmarshal([]byte(headersJson), &headers)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal headers JSON: %w", err)
	}
	if headers == nil {
		return make(map[string]string), nil
	}
	return headers, nil
}

func loadProxies(filePath string) ([]string, error) {
	if filePath == "" {
		return nil, nil // No proxy file specified
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("could not open proxy file %s: %w", filePath, err)
	}
	defer file.Close()

	var proxies []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") { // Allow comments
			// Basic validation: Ensure it parses as a URL later
			_, err := url.Parse(line)
			if err != nil {
				log.Printf("⚠️ Skipping invalid proxy line in %s: '%s' - %v", filePath, line, err)
			} else {
				proxies = append(proxies, line)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading proxy file %s: %w", filePath, err)
	}
	if len(proxies) == 0 {
		log.Printf("⚠️ Proxy file %s was specified but contained no valid proxies.", filePath)
	}
	return proxies, nil
}

// --- Core Logic ---

func handleStartDownloadRequest(w http.ResponseWriter, r *http.Request, numWorkers int) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST method required", http.StatusMethodNotAllowed)
		return
	}

	var cfg Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Printf("❌ Error decoding request body: %v", err)
		http.Error(w, "Invalid configuration JSON", http.StatusBadRequest)
		return
	}

	if cfg.URLTemplate == "" || cfg.OutputDir == "" || cfg.MinZoom > cfg.MaxZoom || cfg.MinZoom < 0 || cfg.MaxZoom < 0 {
		http.Error(w, "Invalid configuration parameters provided", http.StatusBadRequest)
		return
	}

	// Note: Proxies are not configured via UI in this version, only via CLI flag
	go downloadTiles(cfg, numWorkers, nil) // Pass nil for proxies

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("✅ Download task started in the background."))
	log.Println("✅ Download task initiated via Web UI.")
}

func downloadTiles(cfg Config, numWorkers int, proxies []string) {
	log.Printf("🗺️ Starting tile download to '%s' for zoom levels %d to %d...", cfg.OutputDir, cfg.MinZoom, cfg.MaxZoom)
	startTime := time.Now()
	tasks := make(chan TileTask, numWorkers*10)
	wg := sync.WaitGroup{}
	tileCount := 0

	// Create worker pool
	for i := 0; i < numWorkers; i++ {
		var proxyURL *url.URL
		var err error
		if len(proxies) > 0 {
			// Assign proxy to worker - rotate through the list
			proxyStr := proxies[i%len(proxies)]
			proxyURL, err = url.Parse(proxyStr)
			if err != nil {
				log.Printf("❌ Worker %d: Invalid proxy URL '%s', worker will run without proxy: %v", i, proxyStr, err)
				proxyURL = nil // Worker operates without proxy
			}
		}

		wg.Add(1)
		// Pass the specific proxyURL (or nil) to the worker
		go tileWorker(tasks, cfg, &wg, proxyURL, i)
	}

	// Enqueue tasks
	totalTilesEstimated := 0
	for z := cfg.MinZoom; z <= cfg.MaxZoom; z++ {
		xMin, yMin := latLonToTileXY(cfg.BBox[3], cfg.BBox[0], z) // MaxLat, MinLon for top-left
		xMax, yMax := latLonToTileXY(cfg.BBox[1], cfg.BBox[2], z) // MinLat, MaxLon for bottom-right

		if xMax < xMin {
			xMin, xMax = xMax, xMin
		}
		if yMax < yMin {
			yMin, yMax = yMax, yMin
		}

		zoomTileCount := (xMax - xMin + 1) * (yMax - yMin + 1)
		totalTilesEstimated += zoomTileCount
		log.Printf("🔎 Zoom %d: Tile range x[%d..%d], y[%d..%d] (%d tiles)", z, xMin, xMax, yMin, yMax, zoomTileCount)

		for x := xMin; x <= xMax; x++ {
			for y := yMin; y <= yMax; y++ {
				url := strings.ReplaceAll(cfg.URLTemplate, "{z}", fmt.Sprintf("%d", z))
				url = strings.ReplaceAll(url, "{x}", fmt.Sprintf("%d", x))
				url = strings.ReplaceAll(url, "{y}", fmt.Sprintf("%d", y))
				if strings.Contains(url, "{s}") {
					subdomains := []string{"a", "b", "c"}
					sub := subdomains[rand.Intn(len(subdomains))]
					url = strings.ReplaceAll(url, "{s}", sub)
				}

				tasks <- TileTask{Z: z, X: x, Y: y, URL: url}
				tileCount++
			}
		}
	}

	close(tasks)
	log.Printf("⏳ Enqueued %d tiles (~%d estimated). Waiting for workers to finish...", tileCount, totalTilesEstimated)
	wg.Wait()
	duration := time.Since(startTime)
	log.Printf("✅ Tile download complete. %d tasks processed in %v.", tileCount, duration)
}

func tileWorker(tasks <-chan TileTask, cfg Config, wg *sync.WaitGroup, proxyURL *url.URL, workerID int) {
	defer wg.Done()

	// Setup transport for this worker (with or without proxy)
	transport := &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),                // This is nil safe
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false}, // Adjust if needed for source certs
		// Other transport settings like timeouts can be set here
		MaxIdleConnsPerHost: 10, // Improve connection reuse
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	if proxyURL != nil {
		log.Printf("👷 Worker %d using proxy: %s", workerID, proxyURL.Host)
	} else {
		// log.Printf("👷 Worker %d running without proxy", workerID) // Optional: Can be verbose
	}

	for task := range tasks {
		filePath := filepath.Join(cfg.OutputDir, fmt.Sprintf("%d", task.Z), fmt.Sprintf("%d", task.X), fmt.Sprintf("%d.png", task.Y)) // Assuming PNG

		if _, err := os.Stat(filePath); err == nil {
			// log.Printf("☑️ Tile already exists: %s", filePath)
			continue
		}

		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			// Introduce a small random delay *before* the request attempt
			// Delay increases slightly with attempts
			delay := time.Duration(50+rand.Intn(150))*time.Millisecond + time.Duration(attempt*50)*time.Millisecond
			time.Sleep(delay)

			req, err := http.NewRequest("GET", task.URL, nil)
			if err != nil {
				log.Printf("❌ W%d: Error creating request for Z:%d X:%d Y:%d (%s): %v", workerID, task.Z, task.X, task.Y, task.URL, err)
				lastErr = err
				break // Don't retry req creation failure
			}

			for k, v := range cfg.Headers {
				req.Header.Set(k, v)
			}
			if _, ok := cfg.Headers["User-Agent"]; !ok {
				req.Header.Set("User-Agent", fmt.Sprintf("GoTileCacher/1.0 Worker/%d", workerID))
			}

			resp, err := client.Do(req)
			if err != nil {
				lastErr = fmt.Errorf("request failed: %w", err)
				log.Printf("⚠️ W%d Att %d failed for %s: %v", workerID, attempt, task.URL, err)
				// Backoff handled by pre-request delay now + slightly longer sleep for network errors
				time.Sleep(time.Duration(200*attempt) * time.Millisecond)
				continue
			}

			if resp.StatusCode != http.StatusOK {
				bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024)) // Read some body for context
				resp.Body.Close()
				lastErr = fmt.Errorf("bad status: %s", resp.Status)
				log.Printf("⚠️ W%d Att %d got status %s for %s. Body: %s", workerID, attempt, resp.Status, task.URL, string(bodyBytes))
				if resp.StatusCode == 404 || resp.StatusCode == 403 {
					break // Don't retry 404s or 403s
				}
				// Backoff handled by pre-request delay + slightly longer sleep for server errors
				time.Sleep(time.Duration(200*attempt) * time.Millisecond)
				continue
			}

			savePath := filepath.Dir(filePath)
			if err := os.MkdirAll(savePath, os.ModePerm); err != nil {
				log.Printf("❌ W%d: Error creating directory %s: %v", workerID, savePath, err)
				resp.Body.Close()
				lastErr = err
				break // Don't retry fs errors
			}

			file, err := os.Create(filePath + ".tmp") // Write to temp file first
			if err != nil {
				log.Printf("❌ W%d: Error creating temp file %s: %v", workerID, filePath+".tmp", err)
				resp.Body.Close()
				lastErr = err
				break // Don't retry fs errors
			}

			_, copyErr := io.Copy(file, resp.Body)
			closeErr := file.Close() // Close file first
			resp.Body.Close()        // Always close response body

			if copyErr != nil {
				log.Printf("❌ W%d: Error writing tile to %s: %v", workerID, filePath+".tmp", copyErr)
				os.Remove(filePath + ".tmp") // Clean up temp file
				lastErr = copyErr
				time.Sleep(time.Duration(100*attempt) * time.Millisecond) // Short delay before retry
				continue                                                  // Retry copy errors
			}
			if closeErr != nil {
				log.Printf("⚠️ W%d: Error closing temp file %s: %v", workerID, filePath+".tmp", closeErr)
				// Continue anyway, but rename might fail
			}

			// Rename temp file to final destination
			renameErr := os.Rename(filePath+".tmp", filePath)
			if renameErr != nil {
				log.Printf("❌ W%d: Error renaming temp file %s to %s: %v", workerID, filePath+".tmp", filePath, renameErr)
				os.Remove(filePath + ".tmp") // Clean up temp file if rename failed
				lastErr = renameErr
				break // Don't retry rename errors (likely FS issue)
			}

			// log.Printf("💾 W%d: Tile saved: %s", workerID, filePath) // Can be verbose
			lastErr = nil // Success!
			break         // Exit retry loop
		} // End retry loop

		if lastErr != nil {
			log.Printf("❌ W%d: Failed to download tile Z:%d X:%d Y:%d after %d attempts: %v", workerID, task.Z, task.X, task.Y, 3, lastErr)
		}

		// Optional: Keep a small base delay between processing tasks if needed,
		// but the pre-request delay might be sufficient.
		// time.Sleep(time.Duration(10+rand.Intn(20)) * time.Millisecond)

	} // End task loop
}

func latLonToTileXY(lat, lon float64, zoom int) (x, y int) {
	latRad := lat * math.Pi / 180.0
	n := math.Pow(2.0, float64(zoom))
	x = int(math.Floor((lon + 180.0) / 360.0 * n))
	y = int(math.Floor((1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * n))

	// Clamp values to valid range for the zoom level
	maxX := int(n - 1)
	maxY := int(n - 1)
	if x < 0 {
		x = 0
	}
	if x > maxX {
		x = maxX
	}
	if y < 0 {
		y = 0
	}
	if y > maxY {
		y = maxY
	}
	return
}

func serveTileHandler(w http.ResponseWriter, r *http.Request, baseDir string, originURLTemplate string) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	if len(parts) != 3 {
		http.Error(w, "Invalid tile URL format. Expected /z/x/y.ext", http.StatusBadRequest)
		return
	}

	zStr, xStr, yFile := parts[0], parts[1], parts[2]
	yStr := strings.TrimSuffix(yFile, filepath.Ext(yFile)) // Assume extension like .png

	// Validate parts look like numbers and don't contain path traversal
	_, errZ := strconv.Atoi(zStr)
	_, errX := strconv.Atoi(xStr)
	_, errY := strconv.Atoi(yStr)
	if errZ != nil || errX != nil || errY != nil || strings.Contains(yFile, "..") {
		http.Error(w, "Invalid tile coordinates or filename", http.StatusBadRequest)
		return
	}

	tilePath := filepath.Join(baseDir, zStr, xStr, yFile)

	// 1. Try serving the file directly
	// Use os.Stat first to check existence without opening the file handler immediately
	if _, err := os.Stat(tilePath); err == nil {
		// File exists, serve it
		http.ServeFile(w, r, tilePath)
		// log.Printf("Served local: %s", tilePath)
		return
	} else if !os.IsNotExist(err) {
		// Error other than "Not Found" (e.g., permission denied)
		log.Printf("❌ Error accessing tile %s: %v", tilePath, err)
		http.Error(w, "Internal server error accessing tile", http.StatusInternalServerError)
		return
	}

	// 2. File does not exist - try fetching from origin if configured
	if originURLTemplate == "" || originFetchClient == nil {
		// File doesn't exist locally and no origin configured
		http.NotFound(w, r)
		return
	}

	// Construct origin URL
	originURL := strings.ReplaceAll(originURLTemplate, "{z}", zStr)
	originURL = strings.ReplaceAll(originURL, "{x}", xStr)
	originURL = strings.ReplaceAll(originURL, "{y}", yStr) // Use yStr without extension for template usually
	// Handle {s} subdomains if present in origin template
	if strings.Contains(originURL, "{s}") {
		subdomains := []string{"a", "b", "c"}
		sub := subdomains[rand.Intn(len(subdomains))]
		originURL = strings.ReplaceAll(originURL, "{s}", sub)
	}
	// Re-append the extension from the original request if needed by the origin server
	// This assumes the origin URL template doesn't include the extension. Adjust if it does.
	// originURL += filepath.Ext(yFile) // Often not needed if template like .../{y}

	log.Printf("⏳ Cache miss: %s. Fetching from: %s", tilePath, originURL)

	req, err := http.NewRequest("GET", originURL, nil)
	if err != nil {
		log.Printf("❌ Error creating origin request for %s: %v", originURL, err)
		http.Error(w, "Error fetching tile from origin", http.StatusInternalServerError)
		return
	}

	// Add origin headers
	for k, v := range originHeaders {
		req.Header.Set(k, v)
	}
	if _, ok := originHeaders["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "GoTileServerCacheMiss/1.0")
	}

	resp, err := originFetchClient.Do(req)
	if err != nil {
		log.Printf("❌ Error fetching tile %s from origin: %v", originURL, err)
		http.Error(w, "Failed to fetch tile from origin", http.StatusBadGateway) // 502 might be appropriate
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("❌ Origin server returned status %s for %s", resp.Status, originURL)
		// Forward the origin's error status code if desired, or return a generic error
		http.Error(w, fmt.Sprintf("Origin server error: %s", resp.Status), resp.StatusCode)
		return
	}

	// Prepare to save the tile locally while streaming
	savePathDir := filepath.Dir(tilePath)
	if err := os.MkdirAll(savePathDir, os.ModePerm); err != nil {
		log.Printf("❌ Error creating directory %s for saving fetched tile: %v", savePathDir, err)
		// Proceed to stream anyway, but log the error
	}

	// Create a temporary file for saving
	tmpFile, err := os.CreateTemp(savePathDir, filepath.Base(tilePath)+".*.tmp")
	if err != nil {
		log.Printf("❌ Error creating temp file for saving fetched tile %s: %v", tilePath, err)
		// Stream response directly without saving
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type")) // Pass through content type
		io.Copy(w, resp.Body)
		return
	}
	tmpFileName := tmpFile.Name() // Get the name before closing potentially

	// Use TeeReader to write to file while copying to response
	tee := io.TeeReader(resp.Body, tmpFile)

	// Copy data to response writer (which implicitly reads from tee, writing to tmpFile)
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type")) // Pass through content type
	_, copyErr := io.Copy(w, tee)
	closeErr := tmpFile.Close() // Close the temp file

	if copyErr != nil {
		// This error likely means the client disconnected during the stream.
		log.Printf("⚠️ Error streaming tile %s to client (client likely disconnected): %v", originURL, copyErr)
		os.Remove(tmpFileName) // Clean up partial temp file
		return                 // Stop processing, client is gone
	}

	if closeErr != nil {
		log.Printf("❌ Error closing temp file %s after writing: %v", tmpFileName, closeErr)
		os.Remove(tmpFileName) // Clean up potentially corrupt temp file
		return
	}

	// If streaming and saving were successful, rename the temp file
	renameErr := os.Rename(tmpFileName, tilePath)
	if renameErr != nil {
		log.Printf("❌ Error renaming temp file %s to %s: %v", tmpFileName, tilePath, renameErr)
		os.Remove(tmpFileName) // Clean up temp file if rename failed
	} else {
		log.Printf("💾 Tile fetched from origin and saved: %s", tilePath)
	}
}
