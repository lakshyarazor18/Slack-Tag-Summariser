package Repo

import (
	"context"
	"fmt"
	"os"

	"slack-tag-summariser/Models"

	"github.com/jackc/pgx/v5/pgxpool"
)

type User = Models.User

func InitDbPool(dbPool **pgxpool.Pool) error {
	databaseUrl := os.Getenv("DATABASE_URL")
	var dbConnectionError error
	*dbPool, dbConnectionError = pgxpool.New(context.Background(), databaseUrl)
	if dbConnectionError != nil {
		return dbConnectionError
	}
	return nil
}

func SaveUserToDb(userId string, dbPool *pgxpool.Pool) error {

	if dbPool == nil {
		return fmt.Errorf("database pool is not initialized")
	}

	query := `
		INSERT INTO users (user_id)
		VALUES ($1)`

	// Execute using the shared pool
	_, saveUserToDbError := dbPool.Exec(context.Background(), query, userId)
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

	query := `SELECT user_id FROM users`

	rows, dbQueryError := dbPool.Query(context.Background(), query)
	if dbQueryError != nil {
		return nil, dbQueryError
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.UserID); err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return users, nil
}
