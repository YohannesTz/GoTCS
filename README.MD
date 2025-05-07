# GoTCS

A simple vibe-coded and flexible tile caching utility built using Go. This tool allows you to define a bounding box, zoom levels, and tile server parameters to download and cache map tiles locally. It also features a web-based UI using Leaflet to generate commands visually.

> **⚠️ Disclaimer:**  
> This tool is intended **only for educational purposes**. Do **not** use this tool to scrape, spam, or cache content from servers without proper authorization or permission. Abusing this tool may violate the terms of service of tile providers and could lead to legal or ethical consequences.

---

## 🚀 Features

- 📦 Download and cache tiles based on a bounding box and zoom range.
- 🌐 Web-based interface for selecting map areas using Leaflet.
- ⚙️ Configurable tile server URL, headers, and query parameters.
- 🧵 Concurrent downloads for performance.
- 💻 Optional headless mode for CLI-only usage.

---

## 📦 Installation

Make sure you have [Go installed](https://go.dev/doc/install). Then clone the repository and build:

```bash
git clone https://github.com/yourusername/go-tile-cacher.git
cd go-tile-cacher
go build -o tilecacher
```
## 🧑‍💻 Usage

### Start the Web UI:

```bash
./tilecacher
```
Open your browser and go to http://localhost:8080. You can select an area, set zoom levels, and generate the download command.


### Run in Headless Mode

```bash
./tilecacher \
  --url-template="https://tile.server.com/{z}/{x}/{y}.png" \
  --min-zoom=5 \
  --max-zoom=10 \
  --bbox="34.5,9.5,39.0,15.0" \
  --output-dir="./tiles"
```

## ⚠️ Ethical Use
This tool is a powerful utility and should be used responsibly. Always:
 - 📄 Check the terms of service of tile providers.
 - 🤝 Get explicit permission before caching third-party tiles.
 - 💡 Use it for learning, experimentation, or internal testing.

