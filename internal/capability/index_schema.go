package capability

import "database/sql"

// The index is derived from originals. Upgrade in one transaction so removing
// the retired AI queue cannot discard the existing searchable file bodies.
func migrateIndex(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA journal_mode=DELETE; PRAGMA busy_timeout=5000; PRAGMA secure_delete=ON;`); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS files(source TEXT,path TEXT,token TEXT,fingerprint TEXT,body TEXT,folded TEXT,reason TEXT,PRIMARY KEY(source,path));`); err != nil {
		return err
	}
	for _, table := range []string{"extraction_requests", "extraction_members", "extraction_units", "extraction_cache", "extraction_manifest", "extraction_sources", "extraction_control", "organizing_calls", "organizing_settings"} {
		if _, err = tx.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("PRAGMA user_version=5"); err != nil {
		return err
	}
	return tx.Commit()
}

func effectiveFiles(v EffectiveView) map[string]EffectiveFile {
	files := make(map[string]EffectiveFile, len(v.Files))
	for _, f := range v.Files {
		files[f.Path] = f
	}
	return files
}

func (c *Catalog) deleteIndexSource(source string) error {
	_, err := c.index.db.Exec("DELETE FROM files WHERE source=?", source)
	return err
}

// A persisted deletion remains retryable if the index is unavailable.
func (c *Catalog) pruneIndexLocked(item catalogItem) error {
	if c.index == nil || c.index.db == nil {
		return nil
	}
	view, err := item.library.Effective()
	if err != nil {
		return err
	}
	files := effectiveFiles(view)
	rows, err := c.index.db.Query("SELECT path FROM files WHERE source=?", item.source.ID)
	if err != nil {
		return err
	}
	var removed []string
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		if _, exists := files[p]; !exists {
			removed = append(removed, p)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range removed {
		if _, err = c.index.db.Exec("DELETE FROM files WHERE source=? AND path=?", item.source.ID, p); err != nil {
			return err
		}
	}
	return nil
}
