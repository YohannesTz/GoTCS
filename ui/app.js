let boundsLayer = null;
let startPoint = null;
let tempBox = null;
let bounds = null;

const map = L.map('map').setView([9.03, 38.74], 12); // Addis Ababa center
L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png').addTo(map);

map.on('mousedown', (e) => {
    startPoint = e.latlng;

    if (tempBox) {
        map.removeLayer(tempBox);
        tempBox = null;
    }

    map.dragging.disable();

    map.on('mousemove', onMouseMove);
    map.once('mouseup', onMouseUp);
});

["url_template", "output_dir", "min_zoom", "max_zoom", "headers"].forEach(id => {
    const el = document.getElementById(id);
    if (el) {
        el.removeEventListener("input", updateCliPreview);
        el.addEventListener("input", updateCliPreview);
    }
});

updateCliPreview();

document.getElementById("configForm").onsubmit = async function (e) {
    e.preventDefault();
    if (!bounds) {
        alert("Please select a bounding box by clicking and dragging on the map.");
        return;
    }
    if (!document.getElementById("output_dir").value) {
        alert("Please select an output directory.");
        return; // Add check for output dir
    }


    const [minLat, minLon] = [bounds.getSouth(), bounds.getWest()];
    const [maxLat, maxLon] = [bounds.getNorth(), bounds.getEast()];
    let headersObj = {};
    try {
        headersObj = JSON.parse(document.getElementById("headers").value || "{}");
    } catch(err) {
        alert("Headers field contains invalid JSON.");
        return;
    }


    const config = {
        url_template: document.getElementById("url_template").value,
        output_dir: document.getElementById("output_dir").value,
        min_zoom: parseInt(document.getElementById("min_zoom").value),
        max_zoom: parseInt(document.getElementById("max_zoom").value),
        headers: headersObj,
        bbox: [minLon, minLat, maxLon, maxLat], // Correct order for JSON: minLon, minLat, maxLon, maxLat
    };

    // The UI still uses the /start endpoint which expects JSON
    try {
        const res = await fetch("/start", {
            method: "POST",
            headers: {"Content-Type": "application/json"},
            body: JSON.stringify(config)
        });

        const text = await res.text();
        alert(text); // Show response from server (e.g., "✅ Download started...")

        if (!res.ok) {
            log.error("Failed to start download:", text); // Log error details
        }

    } catch (error) {
        alert("Error sending download request: " + error);
        console.error("Error sending download request:", error);
    }
};

function onMouseMove(e) {
    if (!startPoint) return;
    const endPoint = e.latlng;
    const boxBounds = L.latLngBounds(startPoint, endPoint);

    if (tempBox) {
        tempBox.setBounds(boxBounds);
    } else {
        tempBox = L.rectangle(boxBounds, {color: 'red'}).addTo(map);
    }
}

function onMouseUp(e) {
    map.dragging.enable();
    map.off('mousemove', onMouseMove);

    if (!startPoint || !e.latlng) return;

    bounds = L.latLngBounds(startPoint, e.latlng);

    if (boundsLayer) map.removeLayer(boundsLayer);
    boundsLayer = L.rectangle(bounds, {color: 'red'}).addTo(map);

    if (tempBox) {
        map.removeLayer(tempBox);
        tempBox = null;
    }

    const [minLat, minLon] = [bounds.getSouth(), bounds.getWest()];
    const [maxLat, maxLon] = [bounds.getNorth(), bounds.getEast()];
    document.getElementById("bboxDisplay").textContent = `Selected Bounding box: [${minLon.toFixed(6)}, ${minLat.toFixed(6)}, ${maxLon.toFixed(6)}, ${maxLat.toFixed(6)}]`;

    updateCliPreview();
    startPoint = null;
}

function updateCliPreview() {
    console.log("updateCliPreview called");
    const cliPreviewElement = document.getElementById("cli");
    if (!cliPreviewElement) return; // Guard against element not found

    // Get values from the form
    const url_template = document.getElementById("url_template")?.value || "";
    const output_dir = document.getElementById("output_dir")?.value || "";
    const min_zoom_val = document.getElementById("min_zoom")?.value;
    const max_zoom_val = document.getElementById("max_zoom")?.value;
    const headers_val = document.getElementById("headers")?.value || "{}";

    // Basic validation for preview
    if (!output_dir) {
        cliPreviewElement.innerText = "// Please choose an output directory.";
        return;
    }
    if (!url_template) {
        cliPreviewElement.innerText = "// Please enter a URL template.";
        return;
    }
    if (!bounds) {
        cliPreviewElement.innerText = "// Please select a bounding box on the map.";
        return;
    }
    if (!min_zoom_val || !max_zoom_val) {
        cliPreviewElement.innerText = "// Please set Min and Max Zoom levels.";
        return;
    }


    const min_zoom = parseInt(min_zoom_val);
    const max_zoom = parseInt(max_zoom_val);

    // Format BBox for CLI: minLon,minLat,maxLon,maxLat
    const [minLat, minLon] = [bounds.getSouth(), bounds.getWest()];
    const [maxLat, maxLon] = [bounds.getNorth(), bounds.getEast()];
    const bboxString = `${minLon.toFixed(6)},${minLat.toFixed(6)},${maxLon.toFixed(6)},${maxLat.toFixed(6)}`;

    let headersJsonString = "{}";
    try {
        JSON.parse(headers_val); // Validate JSON
        headersJsonString = headers_val.trim() || "{}";
    } catch (e) {
        cliPreviewElement.innerText = "// Invalid JSON in Headers field.";
        return;
    }
    const escapedHeaders = headersJsonString.replace(/'/g, "'\\''");

    const cli = `tile-cacher \\
    --url-template="${url_template}" \\
    --output-dir="${output_dir}" \\
    --bbox="${bboxString}" \\
    --min-zoom=${min_zoom} \\
    --max-zoom=${max_zoom} \\
    --headers='${escapedHeaders}' \\
    --workers=$(nproc || echo ${runtime.NumCPU() * 2}) # Optional: Suggest workers based on cores`; // Assuming 'tile-cacher' is the executable name

    cliPreviewElement.innerText = cli;
}

// Live CLI update on form input
["url_template", "output_dir", "min_zoom", "max_zoom", "headers"].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.addEventListener("input", updateCliPreview);
});

document.getElementById("configForm").onsubmit = async function (e) {
    e.preventDefault();
    if (!bounds) {
        alert("Please select a bounding box by clicking two points on the map.");
        return;
    }

    const [minLat, minLon] = [bounds.getSouth(), bounds.getWest()];
    const [maxLat, maxLon] = [bounds.getNorth(), bounds.getEast()];

    const config = {
        url_template: document.getElementById("url_template").value,
        output_dir: document.getElementById("output_dir").value,
        min_zoom: parseInt(document.getElementById("min_zoom").value),
        max_zoom: parseInt(document.getElementById("max_zoom").value),
        headers: JSON.parse(document.getElementById("headers").value),
        bbox: [minLon, minLat, maxLon, maxLat],
    };

    const cli = document.getElementById("cli").innerText;

    const res = await fetch("/start", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify(config)
    });

    const text = await res.text();
    alert(text);
};

function copyCli() {
    const cliText = document.getElementById("cli").innerText;
    if (cliText.trim() === "") {
        alert("Nothing to copy yet.");
        return;
    }

    navigator.clipboard.writeText(cliText)
        .then(() => alert("CLI command copied to clipboard!"))
        .catch(() => alert("Failed to copy CLI command."));
}

function downloadConfig() {
    const config = {
        tileSource: document.getElementById("tileSource")?.value || "",
        urlTemplate: document.getElementById("url_template").value,
        outputDir: document.getElementById("output_dir").value,
        minZoom: parseInt(document.getElementById("min_zoom").value),
        maxZoom: parseInt(document.getElementById("max_zoom").value),
        headers: JSON.parse(document.getElementById("headers").value || "{}"),
        bbox: bounds ? [bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth()] : null
    };

    const blob = new Blob([JSON.stringify(config, null, 2)], {type: "application/json"});
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = "tile-cacher-config.json";
    link.click();
}

function loadJsonConfig(event) {
    const file = event.target.files[0];
    if (!file) return;

    const reader = new FileReader();
    reader.onload = function (e) {
        try {
            const config = JSON.parse(e.target.result);

            document.getElementById("tileSource").value = config.tileSource || "";
            document.getElementById("url_template").value = config.urlTemplate || "";
            document.getElementById("output_dir").value = config.outputDir || "";
            document.getElementById("min_zoom").value = config.minZoom || "";
            document.getElementById("max_zoom").value = config.maxZoom || "";
            document.getElementById("headers").value = JSON.stringify(config.headers || {}, null, 2);

            if (config.bbox) {
                updateBoundingBox(config.bbox);
            }

            updateCliPreview();
            alert("Configuration loaded from file.");
        } catch (err) {
            console.log("error while loading from file: " + err);
            alert("Invalid JSON file.");
        }
    };
    reader.readAsText(file);
}

function updateBoundingBox(bbox) {
    if (!bbox || bbox.length !== 4) return;
    const sw = L.latLng(bbox[1], bbox[0]);
    const ne = L.latLng(bbox[3], bbox[2]);
    bounds = L.latLngBounds(sw, ne);

    if (boundsLayer) map.removeLayer(boundsLayer);
    boundsLayer = L.rectangle(bounds, {color: 'red'}).addTo(map);
    map.fitBounds(bounds);

    document.getElementById("bboxDisplay").textContent = `Selected Bounding box: [${bbox.join(', ')}]`;
}