// Command reset-admin-password resets the password (and optionally clears 2FA)
// for an existing user — the recovery path when an admin password is forgotten.
// Unlike create-admin, it operates on a user that already exists.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"unicode"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
	"golang.org/x/crypto/bcrypt"
)

func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)

	disableEcho := exec.Command("stty", "-echo")
	disableEcho.Stdin = os.Stdin
	_ = disableEcho.Run()

	defer func() {
		enableEcho := exec.Command("stty", "echo")
		enableEcho.Stdin = os.Stdin
		_ = enableEcho.Run()
		fmt.Println()
	}()

	reader := bufio.NewReader(os.Stdin)
	password, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(password), nil
}

// validatePassword mirrors the panel's password policy (MinPasswordLength = 12).
func validatePassword(password string) error {
	if len(password) < 12 {
		return fmt.Errorf("must be at least 12 characters long")
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, c := range password {
		switch {
		case unicode.IsUpper(c):
			hasUpper = true
		case unicode.IsLower(c):
			hasLower = true
		case unicode.IsDigit(c):
			hasDigit = true
		case unicode.IsPunct(c) || unicode.IsSymbol(c):
			hasSpecial = true
		}
	}
	if !hasUpper {
		return fmt.Errorf("must contain at least one uppercase letter")
	}
	if !hasLower {
		return fmt.Errorf("must contain at least one lowercase letter")
	}
	if !hasDigit {
		return fmt.Errorf("must contain at least one digit")
	}
	if !hasSpecial {
		return fmt.Errorf("must contain at least one special character (e.g. !@#$%%^&*)")
	}
	return nil
}

func main() {
	email := flag.String("email", "", "email of the user whose password to reset (prompts if omitted)")
	clear2FA := flag.Bool("clear-2fa", false, "also disable two-factor authentication for the user")
	flag.Parse()

	cfg, err := config.LoadDefault()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	dbCfg := repository.NewDBConfig(&cfg.Database)
	db, err := repository.InitDB(dbCfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v\n\nEnsure DB_HOST, DB_USER, DB_PASSWORD, DB_NAME environment variables are set.", err)
	}

	addr := strings.TrimSpace(*email)
	if addr == "" {
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Email of the account to reset: ")
		line, rerr := reader.ReadString('\n')
		if rerr != nil {
			log.Fatalf("Failed to read email: %v", rerr)
		}
		addr = strings.TrimSpace(line)
	}
	if addr == "" {
		log.Fatal("Email cannot be empty")
	}

	var user models.User
	if err := db.Where("email = ? AND deleted_at IS NULL", addr).First(&user).Error; err != nil {
		log.Fatalf("No active user found with email %q: %v", addr, err)
	}

	fmt.Printf("Resetting password for %s (role: %s)\n", user.Email, user.Role)

	password, err := readPassword("New password: ")
	if err != nil {
		log.Fatalf("Failed to read password: %v", err)
	}
	confirm, err := readPassword("Confirm new password: ")
	if err != nil {
		log.Fatalf("Failed to read confirmation: %v", err)
	}
	if password != confirm {
		log.Fatal("Passwords do not match")
	}
	if err := validatePassword(password); err != nil {
		log.Fatalf("Weak password: %v", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}

	updates := map[string]interface{}{"password_hash": string(hashed)}
	if *clear2FA {
		// Clear the encrypted TOTP secret and backup codes so a locked-out admin
		// can log in with just the new password.
		updates["two_factor_secret"] = ""
		updates["two_factor_backup_codes"] = ""
	}

	if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		log.Fatalf("Failed to update password: %v", err)
	}

	fmt.Println()
	fmt.Println("Password reset successfully.")
	fmt.Printf("  Email : %s\n", user.Email)
	if *clear2FA {
		fmt.Println("  2FA   : disabled")
	}
}
