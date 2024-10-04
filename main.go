// main.go

package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/kd-hcp/tfe-cli-generator/generator"
	log "github.com/sirupsen/logrus"
)

func main() {
	// Set log level to debug for verbose logging
	log.SetLevel(log.DebugLevel)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	// Clone the go-tfe repository into ./go-tfe
	if err := cloneGoTFE(); err != nil {
		log.Fatalf("Failed to clone go-tfe: %v", err)
	}
	// Ensure cleanup after code generation
	defer cleanupGoTFE()

	log.Info("Starting TFE CLI code generation")

	err := generator.Generate("./go-tfe")
	if err != nil {
		log.Fatalf("Code generation failed: %v", err)
	}

	log.Info("Code generation completed successfully")
}

func cloneGoTFE() error {
	targetDir := "./go-tfe"
	// Remove existing directory if it exists
	if _, err := os.Stat(targetDir); err == nil {
		log.Warnf("Directory %s already exists. Removing it.", targetDir)
		os.RemoveAll(targetDir)
	}
	log.Infof("Cloning go-tfe into %s", targetDir)

	cmd := exec.Command("git", "clone", "https://github.com/hashicorp/go-tfe.git", targetDir)
	cmd.Stdout = log.StandardLogger().Out
	cmd.Stderr = log.StandardLogger().Out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to clone go-tfe: %w", err)
	}

	log.Info("Successfully cloned go-tfe repository")
	return nil
}

func cleanupGoTFE() {
	// Remove the go-tfe directory
	targetDir := "./go-tfe"
	log.Infof("Cleaning up by removing directory %s", targetDir)
	err := os.RemoveAll(targetDir)
	if err != nil {
		log.Errorf("Failed to remove go-tfe directory: %v", err)
	} else {
		log.Infof("Removed go-tfe directory")
	}
}
