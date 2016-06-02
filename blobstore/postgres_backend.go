package main

import (
	"io"
	"os"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/jackc/pgx"
	"github.com/flynn/flynn/pkg/postgres"
)

type PostgresBackend struct{}

func (PostgresBackend) Name() string { return "postgres" }

func (p PostgresBackend) Put(tx *postgres.DBTx, info FileInfo, r io.Reader, append bool) error {
	if !append {
		if err := tx.QueryRow("UPDATE files SET file_oid = lo_create(0) WHERE file_id = $1 RETURNING file_oid", info.ID).Scan(&info.Oid); err != nil {
			return err
		}
	}

	lo, err := tx.LargeObjects()
	if err != nil {
		return err
	}
	obj, err := lo.Open(*info.Oid, pgx.LargeObjectModeWrite)
	if err != nil {
		return err
	}
	if append {
		obj.Seek(info.Size, os.SEEK_SET)
	}
	if _, err := io.Copy(obj, r); err != nil {
		return err
	}

	return nil
}

func (p PostgresBackend) Copy(tx *postgres.DBTx, dst, src FileInfo) error {
	srcFile, err := p.Open(tx, src, false)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	return p.Put(tx, dst, srcFile, false)
}

func (p PostgresBackend) Delete(info FileInfo) error {
	// Do nothing, file data is deleted automatically by trigger when deleted_at is set
	return nil
}

func (p PostgresBackend) Open(tx *postgres.DBTx, info FileInfo, txControl bool) (FileStream, error) {
	if info.Oid == nil {
		return nil, ErrNotFound
	}

	lo, err := tx.LargeObjects()
	if err != nil {
		return nil, err
	}
	obj, err := lo.Open(*info.Oid, pgx.LargeObjectModeRead)
	if err != nil {
		return nil, err
	}
	f := &pgFile{LargeObject: obj, size: info.Size}
	if txControl {
		f.tx = tx
	}
	return f, nil
}

type pgFile struct {
	*pgx.LargeObject
	size     int64
	sizeRead bool
	tx       *postgres.DBTx
}

func (f *pgFile) Close() error {
	if f.tx != nil {
		f.tx.Rollback()
	}
	return nil
}

func (f *pgFile) Seek(offset int64, whence int) (int64, error) {
	// HACK: work around ServeContent length detection, remove when fixed
	if offset == 0 && whence == os.SEEK_END {
		f.sizeRead = true
		return f.size, nil
	} else if f.sizeRead && offset == 0 && whence == os.SEEK_SET {
		return 0, nil
	}
	return f.LargeObject.Seek(offset, whence)
}
