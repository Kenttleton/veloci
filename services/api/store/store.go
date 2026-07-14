package store

import "github.com/jackc/pgx/v5/pgxpool"

// Store holds the database connection pool for all store methods.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a Store backed by the given connection pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}
