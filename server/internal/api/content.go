package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Saagilat/vtt/server/internal/annotation"
)

// CampaignDetail — кампания с составом и ролью смотрящего.
type CampaignDetail struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	Members []Member `json:"members"`
}

// Member — участник кампании.
type Member struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	Role  string `json:"role"`
}

// Image — картинка в библиотеке кампании. Байты наружу не отдаются.
type Image struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Mime   string `json:"mime"`
}

// Scene — сцена: картинка и геометрия из annotation.json.
type Scene struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Image  Image           `json:"image"`
	Grid   json.RawMessage `json:"grid"`
	Walls  json.RawMessage `json:"walls"`
	Doors  json.RawMessage `json:"doors"`
	Areas  json.RawMessage `json:"areas"`
	Light  string          `json:"light"`
	Active bool            `json:"active"`
}

// SceneSummary — сцена в списке, без геометрии.
type SceneSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  Image  `json:"image"`
	Active bool   `json:"active"`
}

// Invite — одноразовый код входа в кампанию.
type Invite struct {
	Code       string `json:"code"`
	CampaignID string `json:"campaign_id"`
	ExpiresAt  string `json:"expires_at"`
}

// TokenPatch — изменение характеристик фишки. nil значит «не трогать».
type TokenPatch struct {
	HP         *int
	HPMax      *int
	Initiative *int
}

// Token — фишка на сцене. Координаты — в пикселях картинки.
type Token struct {
	ID         string  `json:"id"`
	SceneID    string  `json:"scene_id"`
	Name       string  `json:"name"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Color      string  `json:"color"`
	OwnerID    string  `json:"owner_id,omitempty"`
	OwnerLogin string  `json:"owner_login,omitempty"`
	HP         int     `json:"hp"`
	HPMax      int     `json:"hp_max"`
	Initiative int     `json:"initiative"`
}

// ErrInviteNotFound — кода нет.
var (
	ErrInviteNotFound = errors.New("приглашения нет")
	ErrInviteExpired  = errors.New("срок приглашения истёк")
	ErrInviteUsed     = errors.New("приглашение уже использовано")
)

// Content — кампании, картинки и сцены. Отдельный интерфейс, чтобы вход и сессии
// можно было проверять без базы, а контент подключать по мере готовности.
type Content interface {
	CreateCampaignFor(ownerID, name string) (Campaign, error)
	Campaign(accountID, campaignID string) (CampaignDetail, bool)
	CreateInvite(campaignID, createdBy string, expires time.Time) (Invite, error)
	AcceptInvite(code, accountID string, now time.Time) (Campaign, error)
	AddImage(campaignID string, data []byte, mime string, width, height int, sha string) (Image, error)
	CreateScene(campaignID, imageID, name string, annotation []byte, active bool) (Scene, error)
	UpdateSceneAnnotation(campaignID, sceneID string, annotation []byte) (Scene, bool)
	Scene(campaignID, sceneID string) (Scene, bool)
	Scenes(campaignID string) []SceneSummary
	ImageBytes(campaignID, imageID string) ([]byte, string, bool)
	Tokens(campaignID, sceneID string) []Token
	Token(campaignID, sceneID, tokenID string) (Token, bool)
	CreateToken(campaignID, sceneID, ownerID, name string, x, y float64) (Token, bool)
	MoveToken(campaignID, sceneID, tokenID string, x, y float64) (Token, bool)
	SetTokenStats(campaignID, sceneID, tokenID string, hp, hpMax, initiative int) (Token, bool)
	Doors(campaignID, sceneID string) map[string]bool
	SetDoor(campaignID, sceneID, doorID string, open bool) bool
	ExploredMask(campaignID, sceneID, accountID string) []byte
	SaveExploredMask(campaignID, sceneID, accountID string, data []byte) bool
	SetActiveScene(campaignID, sceneID string) bool
	Turn(campaignID string) (tokenID string, round int)
	SetTurn(campaignID, tokenID string, round int) bool
}

// master — роль, которой можно менять кампанию и импортировать карты.
func master(role string) bool {
	return role == RoleOwner || role == RoleMaster
}

func (s *server) listCampaigns(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaigns := s.dir.Campaigns(account.ID)
	if campaigns == nil {
		campaigns = []Campaign{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": campaigns, "next": nil})
}

func (s *server) createCampaign(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "нужно имя кампании")
		return
	}
	campaign, err := s.content.CreateCampaignFor(account.ID, req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "conflict", "кампания не создалась")
		return
	}
	writeJSON(w, http.StatusCreated, campaign)
}

func (s *server) getCampaign(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaign, ok := s.content.Campaign(account.ID, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "кампании нет")
		return
	}
	writeJSON(w, http.StatusOK, campaign)
}

// Инвайт живёт сутки: код передают в чате, а не хранят месяцами.
const inviteTTL = 24 * time.Hour

func (s *server) createInvite(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaign, ok := s.content.Campaign(account.ID, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "кампании нет")
		return
	}
	if campaign.Role != RoleOwner {
		writeError(w, http.StatusForbidden, "forbidden", "приглашает владелец кампании")
		return
	}
	invite, err := s.content.CreateInvite(campaign.ID, account.ID, s.now().Add(inviteTTL))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "conflict", "приглашение не создалось")
		return
	}
	writeJSON(w, http.StatusCreated, invite)
}

func (s *server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	code := strings.ToUpper(strings.TrimSpace(r.PathValue("code")))
	campaign, err := s.content.AcceptInvite(code, account.ID, s.now())
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, campaign)
	case errors.Is(err, ErrInviteNotFound):
		writeError(w, http.StatusNotFound, "not_found", "приглашения нет")
	case errors.Is(err, ErrInviteExpired):
		writeError(w, http.StatusConflict, "conflict", "срок приглашения истёк")
	case errors.Is(err, ErrInviteUsed):
		writeError(w, http.StatusConflict, "conflict", "приглашение уже использовано")
	default:
		writeError(w, http.StatusInternalServerError, "conflict", "приглашение не принято")
	}
}

func readPart(r *http.Request, name string) ([]byte, string, error) {
	file, header, err := r.FormFile(name)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<20))
	if err != nil {
		return nil, "", err
	}
	return data, header.Header.Get("Content-Type"), nil
}

func imageSize(data []byte) (int, int, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	return config.Width, config.Height, nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *server) uploadImage(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaign, ok := s.content.Campaign(account.ID, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "кампании нет")
		return
	}
	if !master(campaign.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "картинки загружает мастер")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "не разобрать загрузку")
		return
	}
	data, mime, err := readPart(r, "image")
	if err != nil || len(data) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "нет файла image")
		return
	}
	width, height, err := imageSize(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "картинка не читается")
		return
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	stored, err := s.content.AddImage(campaign.ID, data, mime, width, height, digest(data))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "conflict", "картинка не сохранилась")
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

func (s *server) importScene(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaign, ok := s.content.Campaign(account.ID, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "кампании нет")
		return
	}
	if !master(campaign.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "импорт делает мастер")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "не разобрать загрузку")
		return
	}
	imageData, mime, err := readPart(r, "image")
	if err != nil || len(imageData) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "нет файла image")
		return
	}
	annotationData, _, err := readPart(r, "annotation")
	if err != nil || len(annotationData) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "нет файла annotation")
		return
	}
	width, height, err := imageSize(imageData)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "картинка не читается")
		return
	}
	document, err := annotation.Parse(annotationData)
	if err != nil {
		var parse *annotation.ParseError
		if errors.As(err, &parse) {
			writeError(w, http.StatusUnprocessableEntity, "validation", parse.Reason)
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "разметка не читается")
		return
	}
	hash := digest(imageData)
	verdict := s.validator.Check(document, &annotation.ImageInfo{
		Width: width, Height: height, Sha256: &hash})
	if !verdict.Good() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": map[string]string{
				"code":          "validation",
				"message":       verdict.Message,
				"contract_code": verdict.Code,
			},
		})
		return
	}
	if mime == "" {
		mime = http.DetectContentType(imageData)
	}
	stored, err := s.content.AddImage(campaign.ID, imageData, mime, width, height, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "conflict", "картинка не сохранилась")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Сцена"
	}
	active := len(s.content.Scenes(campaign.ID)) == 0
	scene, err := s.content.CreateScene(campaign.ID, stored.ID, name, annotationData, active)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "conflict", "сцена не создалась")
		return
	}
	writeJSON(w, http.StatusCreated, scene)
}

func (s *server) listScenes(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaign, ok := s.content.Campaign(account.ID, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "кампании нет")
		return
	}
	scenes := s.content.Scenes(campaign.ID)
	if scenes == nil {
		scenes = []SceneSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": scenes, "next": nil})
}

func (s *server) getImage(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaign, ok := s.content.Campaign(account.ID, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "кампании нет")
		return
	}
	data, mime, ok := s.content.ImageBytes(campaign.ID, r.PathValue("iid"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "картинки нет")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *server) getScene(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaign, ok := s.content.Campaign(account.ID, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "кампании нет")
		return
	}
	scene, ok := s.content.Scene(campaign.ID, r.PathValue("sid"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "сцены нет")
		return
	}
	if !master(campaign.Role) {
		scene.Walls = json.RawMessage("[]")
		scene.Doors = json.RawMessage("[]")
		scene.Areas = json.RawMessage("[]")
	}
	writeJSON(w, http.StatusOK, scene)
}

// updateSceneAnnotation сохраняет разметку, нарисованную мастером в клиенте.
// Проверка та же, что у импорта: клиент не получает доверенной ветки.
func (s *server) updateSceneAnnotation(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaign, ok := s.content.Campaign(account.ID, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "кампании нет")
		return
	}
	if !master(campaign.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "разметку правит мастер")
		return
	}
	scene, ok := s.content.Scene(campaign.ID, r.PathValue("sid"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "сцены нет")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil || len(data) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "пустая разметка")
		return
	}
	document, err := annotation.Parse(data)
	if err != nil {
		var parse *annotation.ParseError
		if errors.As(err, &parse) {
			writeError(w, http.StatusUnprocessableEntity, "validation", parse.Reason)
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "разметка не читается")
		return
	}
	verdict := s.validator.Check(document, &annotation.ImageInfo{
		Width: scene.Image.Width, Height: scene.Image.Height, Sha256: &scene.Image.SHA256})
	if !verdict.Good() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": map[string]string{
				"code":          "validation",
				"message":       verdict.Message,
				"contract_code": verdict.Code,
			},
		})
		return
	}
	updated, ok := s.content.UpdateSceneAnnotation(campaign.ID, scene.ID, data)
	if !ok {
		writeError(w, http.StatusConflict, "conflict", "разметка не сохранилась")
		return
	}
	s.broadcastSceneChanged(campaign.ID, updated.ID, account)
	s.sendVisibility(s.hub.room(campaign.ID), campaign.ID, updated.ID)
	writeJSON(w, http.StatusOK, updated)
}
