package Repo

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"

	"slack-tag-summariser/Models"

	"github.com/jackc/pgx/v5/pgxpool"
)

type User = Models.User

// we need to make the encrypt and decrypt user token helper functions

func decrypt(enc string) (string, error) {
	key := []byte(os.Getenv("TOKEN_ENCRYPTION_KEY"))[:32]

	data, _ := base64.StdEncoding.DecodeString(enc)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

func Encrypt(text string) (string, error) {
	key := []byte(os.Getenv("TOKEN_ENCRYPTION_KEY"))[:32]

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(text), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func InitDbPool(dbPool **pgxpool.Pool) error {
	databaseUrl := os.Getenv("DATABASE_URL")
	var dbConnectionError error
	*dbPool, dbConnectionError = pgxpool.New(context.Background(), databaseUrl)
	if dbConnectionError != nil {
		return dbConnectionError
	}
	return nil
}

func SaveUserToDb(userId string, userToken string, dbPool *pgxpool.Pool) error {

	if dbPool == nil {
		return fmt.Errorf("database pool is not initialized")
	}

	encryptedUserToken, tokenEncryptionErr := Encrypt(userToken)

	if tokenEncryptionErr != nil {
		return tokenEncryptionErr
	}

	query := `
		INSERT INTO users (user_id, access_token)
		VALUES ($1, $2)
	`

	// Execute using the shared pool
	_, saveUserToDbError := dbPool.Exec(context.Background(), query, userId, encryptedUserToken)
	if saveUserToDbError != nil {
		return saveUserToDbError
	}

	return nil
}

func CheckUserInDb(userId string, dbPool *pgxpool.Pool) (bool, error) {

	if dbPool == nil {
		return false, fmt.Errorf("database pool is not initialized")
	}

	query := `
		SELECT COUNT(*) FROM users WHERE user_id = $1`

	var count int
	dbQueryError := dbPool.QueryRow(context.Background(), query, userId).Scan(&count)
	if dbQueryError != nil {
		return false, dbQueryError
	}

	return count > 0, nil
}

func GetInstalledUsers(dbPool *pgxpool.Pool) ([]User, error) {
	if dbPool == nil {
		return nil, fmt.Errorf("database pool is not initialized")
	}
	query := `SELECT user_id, access_token FROM users`

	rows, dbQueryError := dbPool.Query(context.Background(), query)

	if dbQueryError != nil {
		return nil, dbQueryError
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.UserID, &user.UserToken); err != nil {
			fmt.Println("Error scanning user row: %v\n", err)
			continue
		}
		decryptedToken, userTokenDecryptionErr := decrypt(user.UserToken)

		if userTokenDecryptionErr != nil {
			fmt.Println("Error decrypting token for user %s: %v\n", user.UserID, userTokenDecryptionErr)
			continue // Skip this user and continue with the next one
		}
		user.UserToken = decryptedToken
		users = append(users, user)
	}

	return users, nil
}
