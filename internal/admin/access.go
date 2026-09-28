package admin

import (
	"errors"
	"net/http"
	"sort"

	"github.com/tarkiman/claude-whatsapp/internal/access"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
)

// The Admin UI's "Access" card: who may instruct the bot. It only edits the
// access file (internal/access); the bridge re-reads that file on its own.

type accessView struct {
	Config   access.Config `json:"config"`
	Source   string        `json:"source"` // "file" or "env" (no file written yet)
	Error    string        `json:"error,omitempty"`
	BotPhone string        `json:"botPhone,omitempty"`
}

func (s *Server) botPhone() string {
	devs, err := s.gowa.Devices()
	if err != nil || len(devs) == 0 {
		return ""
	}
	u, _ := access.SplitJID(devs[0].JID)
	return u
}

func (s *Server) accessView() accessView {
	v := accessView{Config: s.access.Get(), Source: s.access.Source()}
	if err := s.access.Err(); err != nil {
		v.Error = err.Error()
	}
	return v
}

func (s *Server) handleAccessGet(w http.ResponseWriter, _ *http.Request) {
	v := s.accessView()
	v.BotPhone = s.botPhone()
	writeJSON(w, http.StatusOK, v)
}

type saveAccessRequest struct {
	Mode           access.Mode `json:"mode"`
	PersonalNumber *string     `json:"personalNumber"`
	Team           *struct {
		Group   string          `json:"group"`
		Name    string          `json:"name"`
		Members []access.Member `json:"members"`
	} `json:"team"`
}

func (s *Server) handleAccessSave(w http.ResponseWriter, r *http.Request) {
	var req saveAccessRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	next := s.access.Get()
	if s.access.Err() != nil {
		// A broken file denies everyone; saving replaces it, starting clean.
		next = access.Config{}
	}
	next.Mode = req.Mode
	if req.PersonalNumber != nil {
		next.Personal.Number = *req.PersonalNumber
	}
	if req.Team != nil {
		next.Team = access.Team{Group: req.Team.Group, Name: req.Team.Name, Members: req.Team.Members}
	}
	saved, err := s.access.Save(next)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	v := s.accessView()
	v.Config = saved
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleAccessGroups(w http.ResponseWriter, _ *http.Request) {
	groups, err := s.gowa.Groups()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	if groups == nil {
		groups = []gowa.Group{}
	}
	writeJSON(w, http.StatusOK, groups)
}

type memberView struct {
	Phone      string `json:"phone"`
	LID        string `json:"lid,omitempty"`
	Name       string `json:"name"`
	IsAdmin    bool   `json:"isAdmin"`
	IsBot      bool   `json:"isBot"`
	Approvable bool   `json:"approvable"` // needs a known phone number
	Allowed    bool   `json:"allowed"`
}

// handleAccessMembers lists the group's current members next to their approval
// state. Approval lives in the saved roster, so somebody who joins later shows
// up here un-approved.
func (s *Server) handleAccessMembers(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	if !access.IsGroupJID(group) {
		writeErr(w, http.StatusBadRequest, errors.New("group must be a group JID (…@g.us)"))
		return
	}
	info, err := s.gowa.GroupParticipants(group)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}

	pol := s.access.Get()
	approved := map[string]bool{}
	if pol.Team.Group != "" {
		gu, _ := access.SplitJID(group)
		tu, _ := access.SplitJID(pol.Team.Group)
		if gu == tu {
			for _, m := range pol.Team.Members {
				approved[m.Phone] = true
			}
		}
	}

	bot := s.botPhone()
	out := make([]memberView, 0, len(info.Participants))
	for _, p := range info.Participants {
		phone, err := access.NormalizeNumber(p.PhoneNumber)
		if err != nil {
			phone = ""
		}
		mv := memberView{
			Phone:      phone,
			LID:        access.NormalizeLID(p.LID),
			Name:       access.CleanName(p.DisplayName),
			IsAdmin:    p.IsAdmin,
			IsBot:      phone != "" && phone == bot,
			Approvable: phone != "",
			Allowed:    phone != "" && approved[phone],
		}
		if mv.LID == "" {
			mv.LID = access.NormalizeLID(p.JID)
		}
		out = append(out, mv)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsBot != out[j].IsBot {
			return !out[i].IsBot
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"group": info.GroupID, "name": info.Name, "members": out})
}
