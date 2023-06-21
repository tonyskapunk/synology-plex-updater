package main

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-version"
)

const (
	SYNPKG = "/usr/syno/bin/synopkg"
	SYNOTIFY = "/usr/syno/synobin/synonotify"
	SYNURL   = "https://plex.tv/api/downloads/5.json"
)

type release struct {
	Label    string `json:"label"`
	Build    string `json:"build"`
	Distro   string `json:"distro"`
	URL      string `json:"url"`
	Checksum string `json:"checksum"`
}

type synologyDSM7 struct {
	Version  string    `json:"version"`
	Releases []release `json:"releases"`
}

type nas struct {
	synologyDSM7 `json:"Synology (DSM 7)"`
}

type plex struct {
	Nas nas `json:"nas"`
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		return fallback
	}
	return value
}

func main() {
	// build types:
	// linux-x86
	// linux-x86_64
	// linux-armv7hf_neon
	// linux-aarch64
	// linux-ppc64le
	buildType := getenv("BUILD_TYPE", "linux-x86_64")
	log.Println("Synology Plex Updater - PlexMediaServer for NAS (DSM7)")

	installedVersion := getInstalledVersion()
	log.Println("Installed version: ", installedVersion)

	p := getPlexInfo()
	plexVersion := p.Nas.synologyDSM7.Version
	log.Println("Latest version: ", plexVersion)

	var rel release
	for _, r := range p.Nas.synologyDSM7.Releases {
		if r.Build == buildType {
			rel = r
			break
		}
	}

	iv := strings.Split(installedVersion, "-")[0]
	uv := strings.Split(plexVersion, "-")[0]
	vi, err := version.NewVersion(iv)
	if err != nil {
		log.Fatal(err)
	}
	vu, err := version.NewVersion(uv)
	if err != nil {
		log.Fatal(err)
	}
	if vi.LessThan(vu) {
		log.Println("New version available: ", uv)
		sendNotification("PKGHasUpgrade", "pkg_has_update", "Synology Plex Updater detected a new version: "+uv)
		fp := downloadPlexRelease("./", rel)

		updatePlex(fp)
		updatedVersion := getInstalledVersion()
		log.Println("Updated version: ", updatedVersion)
		sendNotification("PKGHasUpgrade", "pkg_has_update", "Synology Plex Updater has updated PlexMediaServer to version: "+updatedVersion)
	} else {
		log.Println("No new version available")
	}
}

// getInstalledVersion returns the installed version of plex
func getInstalledVersion() string {
	out, err := exec.Command(SYNPKG, "version", "PlexMediaServer").Output()
	if err != nil {
		log.Fatal(err)
	}
	return strings.Split(string(out), "\n")[0]
}

// getPlexInfo returns a plex struct
func getPlexInfo() plex {
	p := plex{}

	cmd := exec.Command("curl", "-s", SYNURL)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}

	if err := json.NewDecoder(stdout).Decode(&p); err != nil {
		log.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		log.Fatal(err)
	}

	return p
}

// checksumFile returns the sha1 checksum of a file
func checksumFile(f string) string {
	file, err := os.Open(f)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	hash := sha1.New()
	if _, err := io.Copy(hash, file); err != nil {
		log.Fatal(err)
	}

	return fmt.Sprintf("%x", hash.Sum(nil))
}

// downloadPlexRelease downloads a plex release and returns the path to the downloaded file
func downloadPlexRelease(dir string, r release) string {
	// check if targe directory already exists
	_, err := os.Stat(dir)
	if os.IsNotExist(err) {
		log.Fatal(err)
	}

	// Parse URL to get filename
	u, err := url.Parse(r.URL)
	if err != nil {
		log.Fatal(err)
	}
	fileName := path.Base(u.Path)
	filePath := filepath.Join(dir, fileName)

	// check if file already exists
	_, err = os.Stat(filePath)
	if !os.IsNotExist(err) {
		log.Println("File already exists: ", filePath)
		log.Println("URL: ", r.URL)

		// check if checksum matches, otherwise delete the local file
		checksum := checksumFile(filePath)
		log.Println("Calculated checksum: ", checksum)
		log.Println("Expected checksum: ", r.Checksum)
		if checksum != r.Checksum {
			log.Println("Checksum mismatch, forcing download")
			err := os.Remove(filePath)
			if err != nil {
				log.Fatal(err)
			}
		} else {
			log.Println("Checksum match")
			return filePath
		}
	}

	// Create and Download the file
	out, err := os.Create(filePath)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	log.Println("Downloading: ", r.URL)
	res, err := http.Get(r.URL)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		log.Fatal(res.Status)
	}

	_, err = io.Copy(out, res.Body)
	if err != nil {
		log.Fatal(err)
	}

	// Verify checksum
	checksum := checksumFile(filePath)
	log.Println("Size: ", res.ContentLength, "bytes")
	log.Println("Calculated checksum: ", checksum)
	log.Println("Expected checksum: ", r.Checksum)

	if checksum != r.Checksum {
		log.Fatal("Checksum mismatch, aborting...")
	}

	return filePath
}

// updatePlexPackage updates the plex package
func updatePlex(f string) {
	log.Println("Stopping PlexMediaServer service")
	out, err := exec.Command(SYNPKG, "stop", "PlexMediaServer").Output()
	if err != nil {
		log.Fatal(err)
	}
	log.Println(strings.Split(string(out), "\n")[0])

	log.Println("Updating PlexMediaServer package")
	out, err = exec.Command(SYNPKG, "install", f).Output()
	if err != nil {
		log.Fatal(err)
	}
	log.Println(strings.Split(string(out), "\n")[0])

	log.Println("Starting PlexMediaServer service")
	out, err = exec.Command(SYNPKG, "start", "PlexMediaServer").Output()
	if err != nil {
		log.Fatal(err)
	}
	log.Println(strings.Split(string(out), "\n")[0])

	log.Println("PlexMediaServer package updated successfully")
}

// sendNotification sends a notification of a particular tag to the Synology Notification Center
func sendNotification(tag string, template string, msg string) {
	j, err := json.Marshal(map[string]interface{}{
		"%" + strings.ToUpper(template) + "%": msg,
	})
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Sending notification: ", SYNOTIFY, tag, string(j))
	out, err := exec.Command(SYNOTIFY, tag, string(j)).Output()
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Notification sent: ", strings.Split(string(out), "\n")[0])
}
