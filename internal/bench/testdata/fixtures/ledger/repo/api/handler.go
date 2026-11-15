// Package api serves the ledger over HTTP.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"example.com/ledger/ledger"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
	maxBodyBytes    = 1 << 16
)

// Handler serves the ledger API.
type Handler struct {
	store *ledger.Store
	mux   *http.ServeMux
}

// New returns a Handler that serves store.
func New(store *ledger.Store) *Handler {
	h := &Handler{store: store, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /accounts", h.listAccounts)
	h.mux.HandleFunc("POST /accounts", h.openAccount)
	h.mux.HandleFunc("GET /accounts/{id}", h.getAccount)
	h.mux.HandleFunc("DELETE /accounts/{id}", h.closeAccount)
	h.mux.HandleFunc("GET /accounts/{id}/statement", h.statement)
	h.mux.HandleFunc("POST /transfers", h.transfer)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

type listResponse struct {
	Accounts []ledger.Account `json:"accounts"`
	Total    int              `json:"total"`
}

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	offset, err := intParam(r, "offset", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit, err := intParam(r, "limit", defaultPageSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit = min(limit, maxPageSize)

	accounts := h.store.List()
	page := []ledger.Account{}
	if offset < len(accounts) {
		page = accounts[offset : offset+limit]
	}
	writeJSON(w, http.StatusOK, listResponse{Accounts: page, Total: len(accounts)})
}

type openRequest struct {
	ID       string `json:"id"`
	Owner    string `json:"owner"`
	Currency string `json:"currency"`
}

func (h *Handler) openAccount(w http.ResponseWriter, r *http.Request) {
	var req openRequest
	if err := decode(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a, err := h.store.Open(req.ID, req.Owner, req.Currency)
	if err != nil {
		writeError(w, ledger.StatusFor(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	a, err := h.store.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, ledger.StatusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *Handler) closeAccount(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Close(r.PathValue("id")); err != nil {
		writeError(w, ledger.StatusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) statement(w http.ResponseWriter, r *http.Request) {
	st, err := h.store.Statement(r.PathValue("id"))
	if err != nil {
		writeError(w, ledger.StatusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type transferRequest struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int64  `json:"amount"`
	Ref    string `json:"ref"`
}

func (h *Handler) transfer(w http.ResponseWriter, r *http.Request) {
	var req transferRequest
	if err := decode(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := h.store.Transfer(req.From, req.To, req.Amount, req.Ref)
	if err != nil {
		writeError(w, status, err)
		return
	}
	w.WriteHeader(status)
}

// intParam reads a non-negative integer query parameter, or returns def
// when the parameter is absent.
func intParam(r *http.Request, name string, def int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return n, nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
