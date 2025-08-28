package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (cfg apiConfig) ensureAssetsDir() error {
	if _, err := os.Stat(cfg.assetsRoot); os.IsNotExist(err) {
		return os.Mkdir(cfg.assetsRoot, 0755)
	}
	return nil
}

func getAssetPath(mediaType string) string {
	base := make([]byte, 32)
	_, err := rand.Read(base)
	if err != nil {
		panic("failed to generate random bytes")
	}
	id := base64.RawURLEncoding.EncodeToString(base)

	ext := mediaTypeExt(mediaType)
	return fmt.Sprintf("%s%s", id, ext)
}

func (cfg apiConfig) getAssetDiskPath(assetPath string) string {
	return filepath.Join(cfg.assetsRoot, assetPath)
}

func (cfg apiConfig) getAssetURL(assetPath string) string {
	return fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, assetPath)
}

func (cfg apiConfig) getObjectURL(key string) string {
	return fmt.Sprintf("%s/%s", os.Getenv("S3CFDISTRIBUTION"), key)
}

func mediaTypeExt(mediaType string) string {
	parts := strings.Split(mediaType, "/")
	if len(parts) != 2 {
		return ".bin"
	}
	return "." + parts[1]
}

func getVideoAspectRatio(filePath string) (string, error) {
	type videoAspect struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}

	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)

	buf := &bytes.Buffer{}
	cmd.Stdout = buf
	err := cmd.Run()
	if err != nil {
		return "", cmd.Err
	}

	var output videoAspect
	err = json.Unmarshal(buf.Bytes(), &output)
	if err != nil {
		return "", errors.New("Unable to decode the aspect ratio")
	}

	if len(output.Streams) < 1 {
		return "", errors.New("No streams found for video")
	}

	width := output.Streams[0].Width
	height := output.Streams[0].Height
	if width == 0 || height == 0 {
		return "", errors.New("Invalid height or width")
	}
	r := float64(width) / float64(height)

	var aspectRatio string
	if math.Abs(r-16.0/9.0) < 0.02 {
		aspectRatio = "16:9"
	} else if math.Abs(r-9.0/16.0) < 0.02 {
		aspectRatio = "9:16"
	} else {
		aspectRatio = "other"
	}
	return aspectRatio, nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputFilePath := filePath + ".processing"
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputFilePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("error processing video: %s, %v", stderr.String(), err)
	}
	
	fileInfo, err := os.Stat(outputFilePath)
	if err != nil {
		return "", fmt.Errorf("could not stat processed file: %v", err)
	}
	if fileInfo.Size() == 0 {
		return "", fmt.Errorf("processed file is empty")
	}

	return outputFilePath, nil
}
