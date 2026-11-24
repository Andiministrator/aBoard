package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// BootstrapRepo provisioniert bei ECHTEM Erstkontakt eines authentifizierten
// Nutzers atomar dessen Identität + persönliches Board (ADR-0066, revidiert
// ADR-0059 §1).
//
// Hintergrund: ADR-0066 ersetzt das Invite-only-Gate durch
// „Open-to-authenticated". Damit ein brandneuer Authelia-Nutzer sofort
// arbeiten kann (statt am alten len(boards)==0→403-Deadlock zu scheitern),
// legt der Server beim ersten Kontakt users-Row + persönliches Board +
// owner-ACL an.
type BootstrapRepo struct {
	db *sql.DB
}

func NewBootstrapRepo(db *sql.DB) *BootstrapRepo {
	return &BootstrapRepo{db: db}
}

// BootstrapNewUser legt – nur falls noch KEINE users-Row für email existiert –
// atomar in EINER Transaktion an: users-Row, persönliches Board (kanonisch
// leer, ADR-0052) und den owner-ACL-Eintrag. Rückgabe `created` ist true,
// wenn neu provisioniert wurde, false wenn die users-Row bereits existierte.
//
// userID/boardID erzeugt der Aufrufer als UUID (Paket-Konvention: die
// uuid-Abhängigkeit lebt im api-Layer, storage bleibt uuid-frei — analog
// UserRepo.Upsert(ctx, uuid.NewString(), …)).
//
// Invarianten (ADR-0066 §1):
//
//   - Atomar & nebenläufigkeitssicher: `BEGIN IMMEDIATE` erwirbt den
//     Write-Lock SOFORT (nicht erst beim ersten Write wie der
//     Go/SQLite-DEFERRED-Default), und der „existiert User?"-Check liegt
//     INNERHALB dieser Transaktion. Ein parallel startender Erst-Request für
//     dieselbe Email blockiert auf dem Lock (busy_timeout aus dem DSN) und
//     liest nach dem Commit die bereits angelegte users-Row → No-op statt
//     Doppel-Board. Exakt das F-191/ADR-0059-§3-Muster (DEFERRED erlaubt
//     Doppel-Apply bei Parallel-Start).
//   - Idempotent: mehrfacher Aufruf für dieselbe Email legt genau einmal an.
//   - Kein Wieder-Anlegen: Trigger ist STRIKT „keine users-Row" — NICHT
//     „User hat 0 Boards". Löscht der Nutzer später sein persönliches Board,
//     bleibt die users-Row bestehen → hier passiert nichts (kein Resurrect).
//   - Eine gepinnte *sql.Conn ist nötig, weil BEGIN/COMMIT/ROLLBACK auf
//     derselben physischen Connection laufen müssen; database/sql kann
//     BEGIN IMMEDIATE nicht über sql.TxOptions ausdrücken (analog
//     RunMigrations).
func (r *BootstrapRepo) BootstrapNewUser(ctx context.Context, userID, boardID, email, name string) (created bool, err error) {
	email = NormalizeEmail(email) // F-260: kanonische Form wie ACL/UserRepo
	if userID == "" || boardID == "" || email == "" {
		return false, fmt.Errorf("bootstrap: userID/boardID/email dürfen nicht leer sein")
	}
	// F-1511 (ADR-0132 §2): "@<domain>" ist ein Domain-Schlüssel, keine Person.
	// Ohne diese Sperre schriebe der Erstkontakt ("@x.de", owner) — eine
	// Domain als Owner, die §3 ausschliesst.
	if IstDomainSubjekt(email) {
		return false, fmt.Errorf("bootstrap: %q ist keine Personen-Adresse", email)
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("bootstrap: conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return false, fmt.Errorf("bootstrap: begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Eigener Context: Rollback muss auch bei abgebrochenem ctx greifen.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Existenz-Check INNERHALB der Immediate-Tx — der Kern der
	// Nebenläufigkeits-Sicherheit (siehe Doc-Kommentar).
	var existingID string
	qerr := conn.QueryRowContext(ctx,
		`SELECT id FROM users WHERE email = ?`, email).Scan(&existingID)
	if qerr == nil {
		// users-Row existiert bereits (Bestandsnutzer oder Race-Verlierer).
		// KEIN Resurrect, kein Doppel-Board. Sauber (No-op) committen.
		if _, cerr := conn.ExecContext(ctx, "COMMIT"); cerr != nil {
			return false, fmt.Errorf("bootstrap: commit (no-op): %w", cerr)
		}
		committed = true
		return false, nil
	}
	if !errors.Is(qerr, sql.ErrNoRows) {
		return false, fmt.Errorf("bootstrap: users-Check: %w", qerr)
	}

	// Echter Erstkontakt → die drei Inserts in DERSELBEN Transaktion.
	createdTS := nowISO()    // ISO-8601 für users.created / acl.created
	createdMS := nowMillis() // Unix-ms für boards.updated_at/created_at

	// #306/ADR-0101 §2: Bootstrap-Admin. Wenn KEIN Admin existiert,
	// bekommt der gerade entstehende User is_admin=TRUE. Der Check läuft
	// INNERHALB der Immediate-Tx — selbst bei zwei parallel anlaufenden
	// brand-neuen Usern gewinnt genau einer den Lock und sieht 0 Admins,
	// der zweite blockiert, sieht danach 1 Admin und bleibt Nicht-Admin.
	// Idempotent: bei späteren Bootstraps (Existing-User-Pfad weiter oben)
	// passiert hier ohnehin nichts mehr.
	var adminCount int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&adminCount); err != nil {
		return false, fmt.Errorf("bootstrap: admin-count: %w", err)
	}
	// F-1855: Ist ABOARD_BOOTSTRAP_OWNER gesetzt und gültig, wird NUR diese
	// Adresse Bootstrap-Admin — nicht, wer zuerst durch den Anmeldedienst
	// kommt. Kommt jemand anderes zuerst, bleibt der Admin-Platz frei; legt
	// die gesetzte Person später ihr Konto an (oder ist sie schon da, wenn
	// die Variable nachträglich gesetzt wird), holt BootstrapAdminNachholen
	// das beim Start nach. Ohne Variable gilt weiter „erste Person“ (ADR-0101
	// §2). Ist sie GESETZT, aber keine Personenadresse (Tippfehler), wird
	// niemand automatisch Admin (Kritiker S-2: der Betreiber wollte eine
	// bestimmte Person — fail closed statt „wer zuerst kommt“).
	isAdmin := 0
	if adminCount == 0 {
		switch owner, gesetzt, ok := BootstrapOwnerAusEnv(); {
		case !gesetzt || owner == email && ok:
			isAdmin = 1
		case !ok:
			log.Printf("aboard-server: FEHLER: %s=%q ist keine Personenadresse — %q wird NICHT Admin, "+
				"es gibt noch keinen. Wert berichtigen und neu starten (F-1855)", BootstrapOwnerEnv, owner, email)
		default:
			log.Printf("aboard-server: %q angemeldet, aber noch kein Admin — Admin wird %s=%q (F-1855)",
				email, BootstrapOwnerEnv, owner)
		}
	}

	if _, err := conn.ExecContext(ctx,
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, ?)`,
		userID, email, name, createdTS, isAdmin); err != nil {
		return false, fmt.Errorf("bootstrap: users insert: %w", err)
	}

	// Persönliches Board: kanonisch leeres Default-Board (ADR-0052), eigene
	// UUID-id, Titel „Board" (1:1 wie Model.createBoard('Board')).
	state := CanonicalEmptyBoardState(boardID, "Board")
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		boardID, "Board", "2", state, createdMS, createdMS); err != nil {
		return false, fmt.Errorf("bootstrap: board insert: %w", err)
	}

	if _, err := conn.ExecContext(ctx,
		`INSERT INTO acl (board_id, user_email, role, created) VALUES (?, ?, ?, ?)`,
		boardID, email, RoleOwner, createdTS); err != nil {
		return false, fmt.Errorf("bootstrap: acl insert: %w", err)
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return false, fmt.Errorf("bootstrap: commit: %w", err)
	}
	committed = true
	return true, nil
}
