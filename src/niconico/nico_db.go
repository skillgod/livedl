package niconico

import (
	"fmt"
	"time"
	"os"
	"log"
	"strings"
	"database/sql"

	"path/filepath"
	"../files"
)

var SelMedia = `SELECT
	seqno, size, data FROM media
	WHERE data IS NOT NULL ORDER BY seqno`

var SelComment = `SELECT
	vpos,
	date,
	date_usec,
	IFNULL(no, -1) AS no,
	IFNULL(anonymity, 0) AS anonymity,
	user_id,
	content,
	IFNULL(mail, "") AS mail,
	IFNULL(premium, 0) AS premium,
	IFNULL(score, 0) AS score,
	thread,
	IFNULL(origin, "") AS origin,
	IFNULL(locale, "") AS locale
	FROM comment
	ORDER BY date2`

func (hls *NicoHls) dbCreate() (err error) {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()

	// table media

	_, err = hls.db.Exec(`
	CREATE TABLE IF NOT EXISTS media (
		seqno     INTEGER PRIMARY KEY NOT NULL UNIQUE,
		current   INTEGER,
		position  REAL,
		notfound  INTEGER,
		noback    INTEGER,
		bandwidth INTEGER,
		size      INTEGER,
		m3u8ms    INTEGER,
		hdrms     INTEGER,
		chunkms   INTEGER,
		data      BLOB
	)
	`)
	if err != nil {
		return
	}

	_, err = hls.db.Exec(`
	CREATE UNIQUE INDEX IF NOT EXISTS media0 ON media(seqno);
	CREATE INDEX IF NOT EXISTS media1 ON media(position);
	---- for debug ----
	CREATE INDEX IF NOT EXISTS media100 ON media(size);
	CREATE INDEX IF NOT EXISTS media101 ON media(notfound);
	CREATE INDEX IF NOT EXISTS media102 ON media(m3u8ms);
	CREATE INDEX IF NOT EXISTS media103 ON media(hdrms);
	CREATE INDEX IF NOT EXISTS media104 ON media(chunkms);
	`)
	if err != nil {
		return
	}

	// table comment

	_, err = hls.db.Exec(`
	CREATE TABLE IF NOT EXISTS comment (
		vpos      INTEGER NOT NULL,
		date      INTEGER NOT NULL,
		date_usec INTEGER NOT NULL,
		date2     INTEGER NOT NULL,
		no        INTEGER,
		anonymity INTEGER,
		user_id   TEXT NOT NULL,
		content   TEXT NOT NULL,
		mail      TEXT,
		premium   INTEGER,
		score     INTEGER,
		thread    INTEGER,
		origin    TEXT,
		locale    TEXT,
		hash      TEXT UNIQUE NOT NULL
	)`)
	if err != nil {
		return
	}

	_, err = hls.db.Exec(`
	CREATE UNIQUE INDEX IF NOT EXISTS comment0 ON comment(hash);
	---- for debug ----
	CREATE INDEX IF NOT EXISTS comment100 ON comment(date2);
	CREATE INDEX IF NOT EXISTS comment101 ON comment(no);
	`)
	if err != nil {
		return
	}


	// kvs media

	_, err = hls.db.Exec(`
	CREATE TABLE IF NOT EXISTS kvs (
		k TEXT PRIMARY KEY NOT NULL UNIQUE,
		v BLOB
	)
	`)
	if err != nil {
		return
	}
	_, err = hls.db.Exec(`
	CREATE UNIQUE INDEX IF NOT EXISTS kvs0 ON kvs(k);
	`)
	if err != nil {
		return
	}

	hls.__dbBegin()

	return
}

//

func (hls *NicoHls) dbCheckSequence(seqno int) (res bool) {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()
	hls.db.QueryRow("SELECT size IS NOT NULL OR notfound <> 0 FROM media WHERE seqno=?", seqno).Scan(&res)
	return
}
func (hls *NicoHls) dbCheckBack(seqno int) (res bool) {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()
	hls.db.QueryRow("SELECT noback <> 0 OR notfound <> 0 FROM media WHERE seqno=?", seqno).Scan(&res)
	return
}
func (hls *NicoHls) dbMarkNoBack(seqno int) {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()

	_, err := hls.db.Exec(`
		INSERT OR IGNORE INTO media (seqno, noback) VALUES(?, 1);
		UPDATE media SET noback = 1 WHERE seqno=?
	`, seqno, seqno)
	if err != nil {
		fmt.Println(err)
	}
}

func (hls *NicoHls) dbGetLastPosition() (res float64) {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()

	hls.db.QueryRow("SELECT position FROM media ORDER BY POSITION DESC LIMIT 1").Scan(&res)
	return
}

func (hls *NicoHls) dbSetM3u8ms() {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()

	_, err := hls.db.Exec(`UPDATE media SET m3u8ms = ? WHERE seqno=?`,
		hls.playlist.m3u8ms,
		hls.playlist.seqNo,
	)
	if err != nil {
		fmt.Println(err)
	}
}
func (hls *NicoHls) dbSetPosition() {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()

	_, err := hls.db.Exec(`UPDATE media SET position = ? WHERE seqno=?`,
		hls.playlist.position,
		hls.playlist.seqNo,
	)
	if err != nil {
		fmt.Println(err)
	}
}

func (hls *NicoHls) __dbBegin() {
	hls.db.Exec(`BEGIN TRANSACTION`)
}
func (hls *NicoHls) __dbCommit(t time.Time) {
	// Never hls.dbMtx.Lock()
	hls.db.Exec(`COMMIT; BEGIN TRANSACTION`)
	hls.lastCommit = t
}
func (hls *NicoHls) dbCommit() {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()

	hls.__dbCommit(time.Now())
}
func (hls *NicoHls) dbExec(query string, args ...interface{}) {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()

//fmt.Println(query)

	if _, err := hls.db.Exec(query, args...); err != nil {
		fmt.Printf("dbExec %#v\n", err)
		hls.db.Exec("COMMIT")
		hls.db.Close()
		os.Exit(1)
	}

	now := time.Now()
	if now.After(hls.lastCommit.Add(15 * time.Second)) {
		log.Printf("Commit: %s\n", hls.dbName)
		hls.__dbCommit(now)
	}
}

func (hls *NicoHls) dbKVSet(k string, v interface{}) {
	query := `INSERT OR REPLACE INTO kvs (k,v) VALUES (?,?)`
	hls.dbExec(query, k, v)
}

func (hls *NicoHls) dbInsert(table string, data map[string]interface{}) {
	var keys []string
	var qs []string
	var args []interface{}

	for k, v := range data {
		keys = append(keys, k)
		qs = append(qs, "?")
		args = append(args, v)
	}
	query := fmt.Sprintf(
		`INSERT OR IGNORE INTO %s (%s) VALUES (%s)`,
		table,
		strings.Join(keys, ","),
		strings.Join(qs, ","),
	)

	hls.dbExec(query, args...)
}

func (hls *NicoHls) dbGetFromWhen() (res_from int, when float64) {
	hls.dbMtx.Lock()
	defer hls.dbMtx.Unlock()
	var date2 int64
	var no int


	hls.db.QueryRow("SELECT date2, no FROM comment ORDER BY date2 ASC LIMIT 1").Scan(&date2, &no)
	res_from = no
	if res_from <= 0 {
		res_from = 1
	}

	if date2 == 0 {
		var endTime float64
		hls.db.QueryRow(`SELECT v FROM kvs WHERE k = "endTime"`).Scan(&endTime)

		when = endTime
	} else {
		when = float64(date2) / (1000 * 1000)
	}

	return
}

func WriteComment(db *sql.DB, fileName string) {

	rows, err := db.Query(SelComment)
	if err != nil {
		log.Println(err)
		return
	}
	defer rows.Close()

	fileName = files.ChangeExtention(fileName, "xml")

	dir := filepath.Dir(fileName)
	base := filepath.Base(fileName)
	base, err = files.GetFileNameNext(base)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fileName = filepath.Join(dir, base)
	f, err := os.Create(fileName)
	if err != nil {
		log.Fatalln(err)
	}
	defer f.Close()
	fmt.Fprintln(f, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprintln(f, `<packet>`)

	for rows.Next() {
		var vpos      int64
		var date      int64
		var date_usec int64
		var no        int64
		var anonymity int64
		var user_id   string
		var content   string
		var mail      string
		var premium   int64
		var score     int64
		var thread    int64
		var origin    string
		var locale    string
		err = rows.Scan(
			&vpos      ,
			&date      ,
			&date_usec ,
			&no        ,
			&anonymity ,
			&user_id   ,
			&content   ,
			&mail      ,
			&premium   ,
			&score     ,
			&thread    ,
			&origin    ,
			&locale    ,
		)
		if err != nil {
			log.Println(err)
			return
		}
		line := fmt.Sprintf(
			`<chat thread="%d" vpos="%d" date="%d" date_usec="%d" user_id="%s"`,
			thread,
			vpos,
			date,
			date_usec,
			user_id,
		)

		if no >= 0 {
			line += fmt.Sprintf(` no="%d"`, no)
		}
		if anonymity != 0 {
			line += fmt.Sprintf(` anonymity="%d"`, anonymity)
		}
		if mail != "" {
			mail = strings.Replace(mail, `"`, "&quot;", -1)
			mail = strings.Replace(mail, "&", "&amp;", -1)
			mail = strings.Replace(mail, "<", "&lt;", -1)
			line += fmt.Sprintf(` mail="%s"`, mail)
		}
		if origin != "" {
			origin = strings.Replace(origin, `"`, "&quot;", -1)
			origin = strings.Replace(origin, "&", "&amp;", -1)
			origin = strings.Replace(origin, "<", "&lt;", -1)
			line += fmt.Sprintf(` origin="%s"`, origin)
		}
		if premium != 0 {
			line += fmt.Sprintf(` premium="%d"`, premium)
		}
		if score != 0 {
			line += fmt.Sprintf(` score="%d"`, score)
		}
		if locale != "" {
			locale = strings.Replace(locale, `"`, "&quot;", -1)
			locale = strings.Replace(locale, "&", "&amp;", -1)
			locale = strings.Replace(locale, "<", "&lt;", -1)
			line += fmt.Sprintf(` locale="%s"`, locale)
		}
		line += ">"
		content = strings.Replace(content, "&", "&amp;", -1)
		content = strings.Replace(content, "<", "&lt;", -1)
		line += content
		line += "</chat>"
		fmt.Fprintln(f, line)
	}
	fmt.Fprintln(f, `</packet>`)
}
