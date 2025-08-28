package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxMemory)

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken((r.Header))
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to get media type from header", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "File is not an mp4 video", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create temporary file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if _, err := io.Copy(tempFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to copy file to temporary location", err)
		return
	}
	tempFile.Sync()

	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not reset file pointer", err)
		return
	}

	assetPath := getAssetPath(mediaType)
	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get aspect ratio", err)
		return
	}

	processedAssetPath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to process video", err)
		return
	}

	reader, err := os.Open(processedAssetPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to read processed video", err)
		return
	}
	defer os.Remove(reader.Name())
	defer reader.Close()

	var orientation string
	switch aspectRatio {
	case "16:9":
		orientation = "landscape"
	case "9:16":
		orientation = "portrait"
	default:
		orientation = "other"
	}

	key := orientation + "/" + assetPath

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(key),
		Body:        reader,
		ContentType: aws.String(mediaType),
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error uploading file to S3", err)
		return
	}

	videoData, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to retrieve video", err)
		return
	}

	if videoData.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not the video owner", err)
		return
	}

	bucketAndKey := fmt.Sprintf("%s,%s", cfg.s3Bucket, key)
	videoData.VideoURL = &bucketAndKey

	err = cfg.db.UpdateVideo(videoData)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video metadata", err)
		return
	}

	signedVideo, err := cfg.dbVideoToSignedVideo(videoData)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to sign video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, signedVideo)

}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)

	ObjectInput := s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key: aws.String(key),
	}

	httpRequest, err := presignClient.PresignGetObject(context.Background(), &ObjectInput, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", fmt.Errorf("Unable to create output request: %v", err)
	}

	return httpRequest.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}

	splitVideo := strings.Split(*video.VideoURL, ",")
	if len(splitVideo) < 2 {
		return database.Video{}, fmt.Errorf("Malformed video url")
	}

	bucket := splitVideo[0]
	key := splitVideo[1]
	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, time.Duration(1 * time.Minute))
	if err != nil {
		return database.Video{}, fmt.Errorf("Unable to generate presigned URL: %v", err)
	}

	video.VideoURL = &presignedURL

	return video, nil
}
