package database

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

// ExportDatabaseJSON dynamically fetches all collections and packages them as JSON bytes
func ExportDatabaseJSON(ctx context.Context) ([]byte, error) {
	if DB == nil {
		return nil, fmt.Errorf("database connection is not initialized")
	}

	// 1. Get all collection names
	collections, err := DB.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to list collections: %w", err)
	}

	backupData := map[string]interface{}{
		"backup_version": 1,
		"timestamp":      time.Now().Format(time.RFC3339),
		"database_name":  DB.Name(),
		"collections":    make(map[string]interface{}),
	}

	// 2. Fetch documents for each collection
	collectionsMap := backupData["collections"].(map[string]interface{})
	for _, collName := range collections {
		coll := DB.Collection(collName)
		cursor, err := coll.Find(ctx, bson.M{})
		if err != nil {
			log.Printf("⚠️ Warning: Failed to query collection %s: %v", collName, err)
			continue
		}
		
		var documents []map[string]interface{}
		if err := cursor.All(ctx, &documents); err != nil {
			log.Printf("⚠️ Warning: Failed to decode collection %s: %v", collName, err)
			cursor.Close(ctx)
			continue
		}
		cursor.Close(ctx)

		// Pack documents
		collectionsMap[collName] = documents
	}

	// 3. Marshal into pretty JSON
	prettyJSON, err := json.MarshalIndent(backupData, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal database export: %w", err)
	}

	return prettyJSON, nil
}

// UploadToGitHub pushes the backup file directly to the private repository via HTTP Contents API
func UploadToGitHub(ctx context.Context, filename string, fileContent []byte) error {
	repo := os.Getenv("GITHUB_BACKUP_REPO")
	token := os.Getenv("GITHUB_BACKUP_TOKEN")

	if repo == "" || token == "" {
		return fmt.Errorf("GitHub backups are unconfigured. Please set GITHUB_BACKUP_REPO and GITHUB_BACKUP_TOKEN in .env")
	}

	// 1. Encode content to Base64
	encodedContent := base64.StdEncoding.EncodeToString(fileContent)

	// 2. Build GitHub API request payload
	commitMessage := fmt.Sprintf("backup: nightly database archive %s", time.Now().Format("2006-01-02 15:04:05"))
	requestBody := map[string]interface{}{
		"message": commitMessage,
		"content": encodedContent,
	}

	jsonPayload, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal github payload: %w", err)
	}

	// 3. Send PUT request to GitHub Contents API
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/backups/%s", repo, filename)
	req, err := http.NewRequestWithContext(ctx, "PUT", apiURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call GitHub API: %w", err)
	}
	defer resp.Body.Close()

	// 4. Verify response
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API returned error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	return nil
}

// RunBackupJob performs the full export and upload cycle
func RunBackupJob() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Println("💾 Starting automated database backup job...")

	// 1. Export MongoDB
	backupBytes, err := ExportDatabaseJSON(ctx)
	if err != nil {
		log.Printf("❌ Backup Job Failed: %v", err)
		return err
	}

	// 2. Build unique filename
	filename := fmt.Sprintf("backup_%s.json", time.Now().Format("2006_01_02_150405"))

	// 3. Push to GitHub
	err = UploadToGitHub(ctx, filename, backupBytes)
	if err != nil {
		log.Printf("❌ Backup Upload Failed: %v", err)
		return err
	}

	log.Printf("✅ Backup successfully archived in GitHub: backups/%s", filename)
	return nil
}

// StartBackupScheduler boots the background ticker checking for 2:00 AM daily uploads
func StartBackupScheduler() {
	go func() {
		// Run every hour to check the time
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		log.Println("⏰ Database Backup Scheduler initialized (Checking daily at 2:00 AM)")

		for range ticker.C {
			now := time.Now()
			// If it's exactly 2:00 AM (or within the 1-hour window)
			if now.Hour() == 2 {
				err := RunBackupJob()
				if err != nil {
					log.Printf("⚠️ Daily backup job failed: %v", err)
				}
			}
		}
	}()
}
