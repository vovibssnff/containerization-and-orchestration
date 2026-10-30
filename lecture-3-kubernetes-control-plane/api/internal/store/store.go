package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Order is one row the api writes and the worker later marks processed.
type Order struct {
	ID        int64  `json:"id"`
	Item      string `json:"item"`
	Processed bool   `json:"processed"`
}

// Store is the postgres access used by both processes.
type Store struct {
	pool *pgxpool.Pool
}

func Connect(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS orders (
			id         bigserial PRIMARY KEY,
			item       text NOT NULL,
			processed  boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now()
		)`)
	return err
}

func (s *Store) Insert(ctx context.Context, item string) (Order, error) {
	var o Order
	err := s.pool.QueryRow(ctx,
		`INSERT INTO orders (item) VALUES ($1) RETURNING id, item, processed`, item,
	).Scan(&o.ID, &o.Item, &o.Processed)
	return o, err
}

func (s *Store) List(ctx context.Context) ([]Order, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, item, processed FROM orders ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.Item, &o.Processed); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) Unprocessed(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE processed = false`).Scan(&n)
	return n, err
}

// ProcessOne marks the oldest unprocessed order done.
// ok is false when the table has nothing left to do.
func (s *Store) ProcessOne(ctx context.Context) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		UPDATE orders SET processed = true
		WHERE id = (
			SELECT id FROM orders
			WHERE processed = false
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
