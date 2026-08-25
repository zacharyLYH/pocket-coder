// SSH key management endpoints: list, register, and delete public keys per
// user. The keys ride on the authenticated email from the session cookie.
package httpapi

import (
	"errors"
	"net/http"

	"sps/internal/sshkeys"
)

func handleListSSHKeys(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := d.Auth.Email(r)
		keys, err := d.SSHKeys.List(email)
		if err != nil {
			writeInternalErr(w, "list ssh keys", err)
			return
		}
		if keys == nil {
			keys = []sshkeys.Key{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
	}
}

func handleAddSSHKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := d.Auth.Email(r)
		var body struct {
			PublicKey string `json:"publicKey"`
			Label     string `json:"label"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		fp, err := d.SSHKeys.Add(email, body.PublicKey, body.Label)
		switch {
		case errors.Is(err, sshkeys.ErrInvalidKey), errors.Is(err, sshkeys.ErrDuplicateKey):
			writeErr(w, http.StatusBadRequest, err.Error())
		case err != nil:
			writeInternalErr(w, "add ssh key", err)
		default:
			_, _ = d.Events.Append("sshkey.added", map[string]any{"fingerprint": fp})
			writeJSON(w, http.StatusCreated, map[string]any{"fingerprint": fp})
		}
	}
}

func handleDeleteSSHKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := d.Auth.Email(r)
		fp := r.PathValue("fingerprint")
		if fp == "" {
			writeErr(w, http.StatusBadRequest, "missing fingerprint")
			return
		}
		if err := d.SSHKeys.Delete(email, fp); err != nil {
			writeInternalErr(w, "delete ssh key", err)
			return
		}
		_, _ = d.Events.Append("sshkey.deleted", map[string]any{"fingerprint": fp})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
