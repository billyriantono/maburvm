package main

import (
	"bufio"
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
	cfg, err := config.LoadDefault()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	dbCfg := repository.NewDBConfig(&cfg.Database)
	db, err := repository.InitDB(dbCfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v\n\nEnsure DB_HOST, DB_USER, DB_PASSWORD, DB_NAME environment variables are set.", err)
	}

	var typeExists int64
	db.Raw("SELECT COUNT(*) FROM pg_type WHERE typname = 'user_role'").Scan(&typeExists)
	if typeExists == 0 {
		if err := db.Exec("CREATE TYPE user_role AS ENUM ('admin', 'client')").Error; err != nil {
			log.Fatalf("Failed to create user_role enum: %v", err)
		}
	}

	if err := db.AutoMigrate(&models.User{}); err != nil {
		log.Fatalf("Failed to run database migration: %v", err)
	}

	var count int64
	if err := db.Model(&models.User{}).
		Where("role = ? AND deleted_at IS NULL", models.RoleAdmin).
		Count(&count).Error; err != nil {
		log.Fatalf("Failed to query database: %v", err)
	}
	if count > 0 {
		fmt.Println("An admin user already exists.")
		fmt.Println("Use the panel web interface or API to manage users.")
		os.Exit(1)
	}

	fmt.Println("=== Create First Admin User ===")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Email: ")
	email, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("Failed to read email: %v", err)
	}
	email = strings.TrimSpace(email)
	if email == "" {
		log.Fatal("Email cannot be empty")
	}

	var emailCount int64
	db.Model(&models.User{}).
		Where("email = ? AND deleted_at IS NULL", email).
		Count(&emailCount)
	if emailCount > 0 {
		log.Fatalf("A user with email %q already exists", email)
	}

	password, err := readPassword("Password: ")
	if err != nil {
		log.Fatalf("Failed to read password: %v", err)
	}
	if password == "" {
		log.Fatal("Password cannot be empty")
	}

	confirm, err := readPassword("Confirm password: ")
	if err != nil {
		log.Fatalf("Failed to read password confirmation: %v", err)
	}
	if password != confirm {
		log.Fatal("Passwords do not match")
	}

	if err := validatePassword(password); err != nil {
		log.Fatalf("Weak password: %v", err)
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}

	user := &models.User{
		Email:        email,
		PasswordHash: string(hashedPassword),
		Role:         models.RoleAdmin,
	}
	if err := db.Create(user).Error; err != nil {
		log.Fatalf("Failed to create admin user: %v", err)
	}

	fmt.Println()
	fmt.Println("Admin user created successfully!")
	fmt.Printf("  Email : %s\n", user.Email)
	fmt.Printf("  Role  : %s\n", user.Role)
	fmt.Printf("  ID    : %s\n", user.ID.String())
}
