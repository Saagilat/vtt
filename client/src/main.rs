use std::collections::{HashMap, HashSet};
use std::fs;
use std::io::Read;
use std::path::PathBuf;
use std::sync::mpsc::{self, Receiver};
use std::thread;

use base64::Engine;
use eframe::egui;
use image::ImageEncoder;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use tungstenite::client::IntoClientRequest;
use tungstenite::http::HeaderValue;

const VERSION: &str = env!("CARGO_PKG_VERSION");

#[derive(Clone, Debug, Deserialize)]
struct Account {
    id: String,
    login: String,
}

#[derive(Deserialize)]
struct SessionResponse {
    token: String,
    account: Account,
}

#[derive(Clone, Debug, Deserialize)]
struct Campaign {
    id: String,
    name: String,
    role: String,
    #[serde(default)]
    archived: bool,
}

#[derive(Clone, Debug, Deserialize)]
struct Member {
    id: String,
    login: String,
    role: String,
}

#[derive(Clone, Debug, Deserialize)]
struct CampaignDetail {
    #[allow(dead_code)]
    id: String,
    #[allow(dead_code)]
    name: String,
    #[allow(dead_code)]
    role: String,
    members: Vec<Member>,
}

#[derive(Deserialize)]
struct Invite {
    code: String,
    #[allow(dead_code)]
    expires_at: String,
}

#[derive(Deserialize)]
struct Me {
    account: Account,
    campaigns: Vec<Campaign>,
}

#[derive(Clone, Debug, Deserialize)]
struct ImageInfo {
    id: String,
    #[allow(dead_code)]
    sha256: String,
    width: u32,
    height: u32,
    #[allow(dead_code)]
    mime: String,
}

#[derive(Clone, Debug, Deserialize)]
struct Character {
    id: String,
    #[serde(default)]
    owner_id: String,
    #[serde(default)]
    owner_login: String,
    name: String,
    #[serde(default)]
    approved: bool,
    #[serde(default)]
    data: Value,
}

#[derive(Clone, Debug, Deserialize)]
struct Note {
    id: String,
    title: String,
    body: String,
    #[serde(default)]
    author_id: String,
    #[serde(default)]
    author_login: String,
    #[serde(default = "default_visibility")]
    visibility: String,
    #[serde(default)]
    updated_at: String,
}

fn default_visibility() -> String {
    "shared".to_string()
}

#[derive(Clone, Debug, Deserialize)]
struct NoteList {
    #[serde(default)]
    items: Vec<Note>,
}

#[derive(Clone, Debug, Deserialize)]
struct CharacterList {
    #[serde(default)]
    items: Vec<Character>,
}

#[derive(Clone, Debug, Deserialize)]
struct SceneSummary {
    id: String,
    name: String,
    #[allow(dead_code)]
    image: ImageInfo,
    active: bool,
}

#[derive(Serialize, Deserialize)]
struct SessionFile {
    base_url: String,
    login: String,
    token: String,
}

#[derive(Clone, Debug, Deserialize)]
struct VisibilityMask {
    blocks: u32,
    visible: String,
    #[serde(default)]
    explored: String,
}

enum ChatEvent {
    Connected,
    History(Vec<String>),
    Message(String),
    Tokens(Vec<Token>),
    TokenPlaced(Token),
    TokenMoved(Token),
    TokenUpdated(Token),
    TokenRemoved(String),
    InitiativeRolled(Token, String, i64),
    CombatReset(Vec<Token>, bool),
    CharacterChanged(Character),
    NoteChanged(Note),
    NoteRemoved(String),
    MembersChanged,
    SceneShown(
        SceneSummary,
        Vec<Token>,
        HashMap<String, bool>,
        Option<String>,
        u32,
    ),
    SceneChanged(String),
    Doors(HashMap<String, bool>),
    DoorSet(String, bool),
    Visibility(VisibilityMask, Option<String>),
    Roll(String),
    Turn(Option<String>, u32),
    Disconnected(String),
    Error(String),
}

fn default_hp() -> i32 {
    10
}

const ABILITY_KEYS: [(&str, &str); 6] = [
    ("str", "СИЛ"),
    ("dex", "ЛОВ"),
    ("con", "ТЕЛ"),
    ("int", "ИНТ"),
    ("wis", "МДР"),
    ("cha", "ХАР"),
];

const SKILLS: [(&str, &str, &str); 18] = [
    ("acrobatics", "Акробатика", "dex"),
    ("animal", "Уход за животными", "wis"),
    ("arcana", "Магия", "int"),
    ("athletics", "Атлетика", "str"),
    ("deception", "Обман", "cha"),
    ("history", "История", "int"),
    ("insight", "Проницательность", "wis"),
    ("intimidation", "Запугивание", "cha"),
    ("investigation", "Расследование", "int"),
    ("medicine", "Медицина", "wis"),
    ("nature", "Природа", "int"),
    ("perception", "Восприятие", "wis"),
    ("performance", "Выступление", "cha"),
    ("persuasion", "Убеждение", "cha"),
    ("religion", "Религия", "int"),
    ("sleight", "Ловкость рук", "dex"),
    ("stealth", "Скрытность", "dex"),
    ("survival", "Выживание", "wis"),
];

fn ability_modifier(score: i32) -> i32 {
    (score - 10).div_euclid(2)
}

fn proficiency_bonus(level: i32) -> i32 {
    2 + (level.max(1) - 1) / 4
}

fn check_formula(bonus: i32) -> String {
    if bonus == 0 {
        "1d20".to_string()
    } else {
        format!("1d20{bonus:+}")
    }
}

fn ability_formula(score: i32) -> String {
    check_formula(ability_modifier(score))
}

fn query_encode(value: &str) -> String {
    let mut out = String::new();
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.' | b'~') {
            out.push(byte as char);
        } else {
            out.push_str(&format!("%{byte:02X}"));
        }
    }
    out
}

fn highlight_ranges(text: &str, query: &str) -> Vec<(usize, usize)> {
    let query = query.trim().to_lowercase();
    if query.is_empty() {
        return Vec::new();
    }
    let lower = text.to_lowercase();
    let mut ranges = Vec::new();
    let mut from = 0;
    while let Some(position) = lower[from..].find(&query) {
        let start = from + position;
        let end = start + query.len();
        if text.is_char_boundary(start) && text.is_char_boundary(end) {
            ranges.push((start, end));
        }
        from = end.max(start + 1);
        if from >= lower.len() {
            break;
        }
    }
    ranges
}

fn search_highlight(text: &str, query: &str, size: f32) -> egui::text::LayoutJob {
    let base = egui::TextFormat {
        font_id: egui::FontId::proportional(size),
        color: WARM_TEXT,
        ..Default::default()
    };
    let mut hit = base.clone();
    hit.background = WARM_ACCENT_DARK;
    let mut job = egui::text::LayoutJob::default();
    let mut position = 0;
    for (start, end) in highlight_ranges(text, query) {
        if start > position {
            job.append(&text[position..start], 0.0, base.clone());
        }
        job.append(&text[start..end], 0.0, hit.clone());
        position = end;
    }
    if position < text.len() {
        job.append(&text[position..], 0.0, base);
    }
    job
}

fn search_snippet(text: &str, query: &str, before: usize, after: usize) -> String {
    let query = query.trim().to_lowercase();
    if query.is_empty() {
        return String::new();
    }
    let lower = text.to_lowercase();
    let Some(index) = lower.find(&query) else {
        return String::new();
    };
    let index = if text.is_char_boundary(index) {
        index
    } else {
        0
    };
    let start = text[..index]
        .char_indices()
        .rev()
        .nth(before)
        .map(|(position, _)| position)
        .unwrap_or(0);
    let end = text[index..]
        .char_indices()
        .nth(after)
        .map(|(position, _)| index + position)
        .unwrap_or(text.len());
    let mut snippet = text[start..end].replace(['\n', '\r'], " ");
    if start > 0 {
        snippet.insert(0, '…');
    }
    if end < text.len() {
        snippet.push('…');
    }
    snippet
}

fn sheet_flag(data: &Value, section: &str, key: &str) -> bool {
    data.get(section)
        .and_then(|section| section.get(key))
        .and_then(Value::as_bool)
        .unwrap_or(false)
}

fn sheet_ability(data: &Value, key: &str) -> i32 {
    data.get("abilities")
        .and_then(|abilities| abilities.get(key))
        .and_then(Value::as_i64)
        .map(|value| value.clamp(1, 30) as i32)
        .unwrap_or(10)
}

fn ability_cell(ui: &mut egui::Ui, label: &str, score: &mut i32) -> bool {
    ui.vertical(|ui| {
        ui.label(egui::RichText::new(label).size(13.0).color(WARM_MUTED));
        ui.add(egui::DragValue::new(score).range(1..=30));
        ui.small_button(format!("d20 {:+}", ability_modifier(*score)))
            .on_hover_text(format!("бросить проверку {label}"))
            .clicked()
    })
    .inner
}

fn character_hp(character: &Character) -> (i32, i32) {
    let hp = character
        .data
        .get("hp")
        .and_then(Value::as_i64)
        .unwrap_or(10) as i32;
    let hp_max = character
        .data
        .get("max_hp")
        .and_then(Value::as_i64)
        .filter(|value| *value > 0)
        .unwrap_or(10) as i32;
    (hp.clamp(0, hp_max), hp_max)
}

#[derive(Clone, Debug, Deserialize)]
struct Token {
    id: String,
    scene_id: String,
    name: String,
    x: f64,
    y: f64,
    #[serde(default)]
    color: String,
    #[serde(default)]
    owner_id: String,
    #[serde(default)]
    owner_login: String,
    #[serde(default)]
    character_id: String,
    #[serde(default)]
    character_name: String,
    #[serde(default = "default_hp")]
    hp: i32,
    #[serde(default = "default_hp")]
    hp_max: i32,
    #[serde(default)]
    initiative: i32,
    #[serde(default)]
    darkvision_ft: i32,
}

#[derive(Clone, Debug, Deserialize)]
struct Scene {
    #[allow(dead_code)]
    id: String,
    name: String,
    image: ImageInfo,
    grid: Value,
    walls: Value,
    doors: Value,
    areas: Value,
    light: String,
    #[allow(dead_code)]
    active: bool,
    #[serde(default)]
    version: u32,
}

#[derive(Clone, Copy, PartialEq)]
enum Screen {
    Login,
    Campaigns,
    Scenes,
    Scene,
}

#[derive(Clone, Copy, PartialEq)]
enum CampaignAction {
    None,
    Create,
    Join,
}

#[derive(Clone, Copy, PartialEq)]
enum PanelTab {
    Chat,
    Combat,
    Sheet,
    Journal,
    Master,
}

#[derive(Clone, Copy, PartialEq)]
enum EditMode {
    Off,
    Wall,
    Door,
}

struct App {
    base_url: String,
    login: String,
    password: String,
    token: Option<String>,
    account: Option<Account>,
    campaigns: Vec<Campaign>,
    show_archived: bool,
    campaign_action: CampaignAction,
    campaign: Option<Campaign>,
    campaign_detail: Option<CampaignDetail>,
    new_campaign: String,
    scenes: Vec<SceneSummary>,
    scene: Option<Scene>,
    texture: Option<egui::TextureHandle>,
    pending_image: Option<Vec<u8>>,
    screen: Screen,
    error: String,
    notice: String,
    zoom: f32,
    offset: egui::Vec2,
    image_path: String,
    annotation_path: String,
    scene_name: String,
    join_code: String,
    invite_code: String,
    characters: Vec<Character>,
    selected_character: Option<String>,
    sheet_name: String,
    sheet_class: String,
    sheet_level: i32,
    sheet_hp: i32,
    sheet_hp_max: i32,
    sheet_str: i32,
    sheet_dex: i32,
    sheet_con: i32,
    sheet_int: i32,
    sheet_wis: i32,
    sheet_cha: i32,
    sheet_inventory: String,
    sheet_spells: String,
    sheet_notes: String,
    save_profs: HashSet<String>,
    skill_profs: HashSet<String>,
    new_character_name: String,
    notes: Vec<Note>,
    selected_note: Option<String>,
    note_title: String,
    note_body: String,
    note_private: bool,
    note_query: String,
    new_note_title: String,
    new_note_private: bool,
    confirm_owner: Option<String>,
    chat_rx: Option<Receiver<ChatEvent>>,
    chat_input: String,
    chat_channel: String,
    chat_to: String,
    chat_to_login: String,
    chat_lines: Vec<String>,
    chat_status: String,
    tokens: Vec<Token>,
    token_name: String,
    token_owner: String,
    token_character: String,
    token_darkvision: i32,
    roll_formula: String,
    preview_account: String,
    preview_name: Option<String>,
    visibility: Option<VisibilityMask>,
    pending_visibility: Option<VisibilityMask>,
    fog_texture: Option<egui::TextureHandle>,
    turn_token: Option<String>,
    round: u32,
    panel_tab: PanelTab,
    edit_mode: EditMode,
    edit_points: Vec<(f32, f32)>,
    show_settings: bool,
    hint_dismissed: bool,
    drag_token: Option<String>,
    confirm_token_remove: Option<String>,
    reconnect_at: Option<std::time::Instant>,
    reconnect_attempts: u32,
    doors: HashMap<String, bool>,
    #[allow(dead_code)]
    install_dir: Option<PathBuf>,
}

impl App {
    fn new(
        cc: &eframe::CreationContext<'_>,
        base_url: String,
        install_dir: Option<PathBuf>,
    ) -> Self {
        cc.egui_ctx.set_visuals(egui::Visuals::dark());
        apply_theme(&cc.egui_ctx);
        let mut app = Self {
            base_url,
            login: String::new(),
            password: String::new(),
            token: None,
            account: None,
            campaigns: Vec::new(),
            show_archived: false,
            campaign_action: CampaignAction::None,
            campaign: None,
            campaign_detail: None,
            new_campaign: String::new(),
            scenes: Vec::new(),
            scene: None,
            texture: None,
            pending_image: None,
            screen: Screen::Login,
            error: String::new(),
            notice: String::new(),
            zoom: 0.0,
            offset: egui::Vec2::ZERO,
            image_path: String::new(),
            annotation_path: String::new(),
            scene_name: "Сцена".to_string(),
            join_code: String::new(),
            invite_code: String::new(),
            chat_rx: None,
            chat_input: String::new(),
            chat_channel: "table".to_string(),
            chat_to: String::new(),
            chat_to_login: String::new(),
            chat_lines: Vec::new(),
            chat_status: String::new(),
            tokens: Vec::new(),
            token_name: "Фишка".to_string(),
            token_owner: String::new(),
            token_character: String::new(),
            token_darkvision: 0,
            roll_formula: "1d20".to_string(),
            preview_account: String::new(),
            preview_name: None,
            characters: Vec::new(),
            selected_character: None,
            sheet_name: "Персонаж".to_string(),
            sheet_class: String::new(),
            sheet_level: 1,
            sheet_hp: 10,
            sheet_hp_max: 10,
            sheet_str: 10,
            sheet_dex: 10,
            sheet_con: 10,
            sheet_int: 10,
            sheet_wis: 10,
            sheet_cha: 10,
            sheet_inventory: String::new(),
            sheet_spells: String::new(),
            sheet_notes: String::new(),
            save_profs: HashSet::new(),
            skill_profs: HashSet::new(),
            new_character_name: String::new(),
            notes: Vec::new(),
            selected_note: None,
            note_title: String::new(),
            note_body: String::new(),
            note_private: false,
            note_query: String::new(),
            new_note_title: String::new(),
            new_note_private: false,
            confirm_owner: None,
            visibility: None,
            pending_visibility: None,
            fog_texture: None,
            turn_token: None,
            round: 0,
            panel_tab: PanelTab::Chat,
            edit_mode: EditMode::Off,
            edit_points: Vec::new(),
            show_settings: false,
            hint_dismissed: false,
            drag_token: None,
            confirm_token_remove: None,
            reconnect_at: None,
            reconnect_attempts: 0,
            doors: HashMap::new(),
            install_dir,
        };
        app.resume_session();
        app
    }

    fn session_file(&self) -> Option<PathBuf> {
        self.install_dir
            .as_ref()
            .map(|dir| dir.join("session.json"))
    }

    fn resume_session(&mut self) {
        let path = match self.session_file() {
            Some(path) => path,
            None => return,
        };
        let text = match fs::read_to_string(&path) {
            Ok(text) => text,
            Err(_) => return,
        };
        let saved: SessionFile = match serde_json::from_str(&text) {
            Ok(saved) => saved,
            Err(_) => return,
        };
        self.base_url = saved.base_url;
        self.login = saved.login;
        self.token = Some(saved.token);
        if let Err(error) = self.load_campaigns() {
            self.token = None;
            self.screen = Screen::Login;
            self.notice = "сессия истекла: ".to_string() + &error;
        } else {
            self.notice = "сессия восстановлена".to_string();
        }
    }

    fn save_session(&mut self) {
        let (path, token, login) = match (&self.session_file(), &self.token, &self.account) {
            (Some(path), Some(token), Some(account)) => {
                (path.clone(), token.clone(), account.login.clone())
            }
            _ => return,
        };
        if let Some(parent) = path.parent() {
            let _ = fs::create_dir_all(parent);
        }
        let saved = SessionFile {
            base_url: self.base_url.clone(),
            login,
            token,
        };
        if let Ok(text) = serde_json::to_string_pretty(&saved) {
            let _ = fs::write(path, text);
        }
    }

    fn logout(&mut self) {
        if let Some(path) = self.session_file() {
            let _ = fs::remove_file(path);
        }
        self.token = None;
        self.account = None;
        self.campaigns.clear();
        self.campaign = None;
        self.campaign_action = CampaignAction::None;
        self.scenes.clear();
        self.scene = None;
        self.texture = None;
        self.chat_rx = None;
        self.chat_lines.clear();
        self.chat_status.clear();
        self.characters.clear();
        self.selected_character = None;
        self.notes.clear();
        self.selected_note = None;
        self.tokens.clear();
        self.drag_token = None;
        self.confirm_token_remove = None;
        self.reconnect_at = None;
        self.reconnect_attempts = 0;
        self.doors.clear();
        self.screen = Screen::Login;
        self.notice = "вы вышли".to_string();
    }

    fn get_json<T: DeserializeOwned>(&self, path: &str) -> Result<T, String> {
        let token = self.token.as_deref().unwrap_or("");
        let response = ureq::get(&format!("{}{}", self.base_url, path))
            .set("Authorization", &format!("Bearer {token}"))
            .call()
            .map_err(format_error)?;
        response.into_json::<T>().map_err(|error| error.to_string())
    }

    fn post_json<T: DeserializeOwned>(&self, path: &str, body: Value) -> Result<T, String> {
        let request = ureq::post(&format!("{}{}", self.base_url, path));
        let request = match &self.token {
            Some(token) => request.set("Authorization", &format!("Bearer {token}")),
            None => request,
        };
        request
            .send_json(body)
            .map_err(format_error)?
            .into_json::<T>()
            .map_err(|error| error.to_string())
    }

    fn put_json<T: DeserializeOwned>(&self, path: &str, body: Value) -> Result<T, String> {
        let request = ureq::put(&format!("{}{}", self.base_url, path));
        let request = match &self.token {
            Some(token) => request.set("Authorization", &format!("Bearer {token}")),
            None => request,
        };
        request
            .send_json(body)
            .map_err(format_error)?
            .into_json::<T>()
            .map_err(|error| error.to_string())
    }

    fn patch_json<T: DeserializeOwned>(&self, path: &str, body: Value) -> Result<T, String> {
        let request = ureq::patch(&format!("{}{}", self.base_url, path));
        let request = match &self.token {
            Some(token) => request.set("Authorization", &format!("Bearer {token}")),
            None => request,
        };
        request
            .send_json(body)
            .map_err(format_error)?
            .into_json::<T>()
            .map_err(|error| error.to_string())
    }

    fn post_empty(&self, path: &str) -> Result<(), String> {
        let token = self.token.as_deref().unwrap_or("");
        ureq::post(&format!("{}{}", self.base_url, path))
            .set("Authorization", &format!("Bearer {token}"))
            .call()
            .map_err(format_error)?;
        Ok(())
    }

    fn delete_request(&self, path: &str) -> Result<(), String> {
        let token = self.token.as_deref().unwrap_or("");
        ureq::delete(&format!("{}{}", self.base_url, path))
            .set("Authorization", &format!("Bearer {token}"))
            .call()
            .map_err(format_error)?;
        Ok(())
    }

    fn login(&mut self) {
        self.error.clear();
        match self.post_json::<SessionResponse>(
            "/v1/sessions",
            serde_json::json!({"login": self.login, "password": self.password}),
        ) {
            Ok(session) => {
                self.token = Some(session.token);
                self.account = Some(session.account);
                self.password.clear();
                self.save_session();
                if let Err(error) = self.load_campaigns() {
                    self.error = error;
                }
            }
            Err(error) => self.error = error,
        }
    }

    fn load_campaigns(&mut self) -> Result<(), String> {
        let path = if self.show_archived {
            "/v1/me?archived=1"
        } else {
            "/v1/me"
        };
        let me: Me = self.get_json(path)?;
        self.account = Some(me.account);
        self.campaigns = me.campaigns;
        self.screen = Screen::Campaigns;
        Ok(())
    }

    fn open_campaign(&mut self, campaign: Campaign) {
        self.campaign = Some(campaign.clone());
        self.campaign_detail = None;
        self.scenes.clear();
        self.scene = None;
        self.texture = None;
        self.pending_image = None;
        self.invite_code.clear();
        self.confirm_owner = None;
        self.token_owner.clear();
        self.preview_account.clear();
        self.preview_name = None;
        self.chat_channel = "table".to_string();
        self.chat_to.clear();
        self.chat_to_login.clear();
        self.panel_tab = PanelTab::Chat;
        self.hint_dismissed = false;
        match self.get_json::<CampaignDetail>(&format!("/v1/campaigns/{}", campaign.id)) {
            Ok(detail) => self.campaign_detail = Some(detail),
            Err(error) => self.error = error,
        }
        match self.get_json::<Value>(&format!("/v1/campaigns/{}/scenes", campaign.id)) {
            Ok(value) => {
                let items = value
                    .get("items")
                    .cloned()
                    .unwrap_or_else(|| Value::Array(Vec::new()));
                match serde_json::from_value::<Vec<SceneSummary>>(items) {
                    Ok(scenes) => {
                        self.scenes = scenes;
                        self.screen = Screen::Scenes;
                    }
                    Err(error) => self.error = error.to_string(),
                }
            }
            Err(error) => self.error = error,
        }
        if campaign.role != "owner" && campaign.role != "master" {
            if let Some(active) = self.scenes.iter().find(|scene| scene.active).cloned() {
                self.open_scene(active);
            } else {
                self.notice = "мастер ещё не показал сцену".to_string();
            }
        }
    }

    fn refresh_campaign_detail(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        match self.get_json::<CampaignDetail>(&format!("/v1/campaigns/{}", campaign.id)) {
            Ok(detail) => {
                if let Some(current) = &mut self.campaign {
                    current.role = detail.role.clone();
                }
                self.campaign_detail = Some(detail);
            }
            Err(error) => {
                self.error = error;
                self.campaign = None;
                self.campaign_detail = None;
                self.scenes.clear();
                self.scene = None;
                self.screen = Screen::Campaigns;
                let _ = self.load_campaigns();
            }
        }
    }

    fn change_member_role(&mut self, account_id: &str, role: &str) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        match self.patch_json::<Value>(
            &format!("/v1/campaigns/{}/members/{}", campaign.id, account_id),
            serde_json::json!({"role": role}),
        ) {
            Ok(_) => {
                self.notice = format!("роль изменена: {}", role_name(role));
                self.refresh_campaign_detail();
            }
            Err(error) => self.error = error,
        }
    }

    fn transfer_ownership(&mut self, account_id: &str) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        match self.post_json::<Value>(
            &format!("/v1/campaigns/{}/owner", campaign.id),
            serde_json::json!({"account_id": account_id}),
        ) {
            Ok(_) => {
                self.confirm_owner = None;
                self.notice = "владение кампанией передано".to_string();
                self.refresh_campaign_detail();
            }
            Err(error) => self.error = error,
        }
    }

    fn remove_member(&mut self, account_id: &str) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        match self.delete_request(&format!(
            "/v1/campaigns/{}/members/{}",
            campaign.id, account_id
        )) {
            Ok(()) => {
                self.notice = "участник убран из кампании".to_string();
                self.refresh_campaign_detail();
            }
            Err(error) => self.error = error,
        }
    }

    fn archive_campaign(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        match self.post_empty(&format!("/v1/campaigns/{}/archive", campaign.id)) {
            Ok(()) => {
                self.campaign = None;
                self.campaign_detail = None;
                self.scenes.clear();
                self.scene = None;
                self.show_archived = false;
                if let Err(error) = self.load_campaigns() {
                    self.error = error;
                }
                self.notice = format!("кампания «{}» в архиве", campaign.name);
            }
            Err(error) => self.error = error,
        }
    }

    fn unarchive_campaign(&mut self, campaign: &Campaign) {
        match self.post_empty(&format!("/v1/campaigns/{}/unarchive", campaign.id)) {
            Ok(()) => {
                if let Err(error) = self.load_campaigns() {
                    self.error = error;
                }
                self.notice = format!("кампания «{}» возвращена", campaign.name);
            }
            Err(error) => self.error = error,
        }
    }

    fn create_campaign(&mut self) {
        let name = self.new_campaign.trim().to_string();
        if name.is_empty() {
            self.error = "введите название кампании".to_string();
            return;
        }
        match self.post_json::<Campaign>("/v1/campaigns", serde_json::json!({"name": name})) {
            Ok(campaign) => {
                self.new_campaign.clear();
                self.notice = format!("кампания «{}» создана", campaign.name);
                if let Err(error) = self.load_campaigns() {
                    self.error = error;
                    return;
                }
                self.open_campaign(campaign);
            }
            Err(error) => self.error = error,
        }
    }

    fn create_demo(&mut self) {
        if let Some(existing) = self
            .campaigns
            .iter()
            .find(|campaign| campaign.name == "Демо: таверна")
            .cloned()
        {
            self.open_campaign(existing);
            self.open_first_scene();
            self.notice = "демо-кампания уже есть — открываю стол".to_string();
            return;
        }
        let campaign = match self.post_json::<Campaign>(
            "/v1/campaigns",
            serde_json::json!({"name": "Демо: таверна"}),
        ) {
            Ok(campaign) => campaign,
            Err(error) => {
                self.error = error;
                return;
            }
        };
        let (image, annotation) = demo_scene();
        let (body, content_type) = multipart_body(&image, &annotation, "Таверна");
        match self.post_multipart(
            &format!("/v1/campaigns/{}/scenes:import", campaign.id),
            body,
            content_type,
        ) {
            Ok(_) => {
                if let Err(error) = self.load_campaigns() {
                    self.error = error;
                    return;
                }
                self.open_campaign(campaign);
                self.open_first_scene();
                self.notice = "демо-стол готов: сцена «Таверна»".to_string();
            }
            Err(error) => self.error = error,
        }
    }

    fn open_first_scene(&mut self) {
        if let Some(scene) = self.scenes.first().cloned() {
            self.open_scene(scene);
        }
    }

    fn create_invite(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        match self.post_json::<Invite>(
            &format!("/v1/campaigns/{}/invites", campaign.id),
            serde_json::json!({"code": true}),
        ) {
            Ok(invite) => {
                self.invite_code = invite.code.clone();
                self.notice = format!(
                    "код приглашения: {}. Передайте его игроку, он действует сутки",
                    invite.code
                );
            }
            Err(error) => self.error = error,
        }
    }

    fn accept_invite(&mut self) {
        let code = self.join_code.trim().to_uppercase();
        if code.is_empty() {
            self.error = "введите код приглашения".to_string();
            return;
        }
        match self
            .post_json::<Campaign>(&format!("/v1/invites/{code}/accept"), serde_json::json!({}))
        {
            Ok(campaign) => {
                self.join_code.clear();
                self.notice = format!("вы в кампании «{}»", campaign.name);
                if let Err(error) = self.load_campaigns() {
                    self.error = error;
                }
            }
            Err(error) => self.error = error,
        }
    }

    fn open_scene(&mut self, summary: SceneSummary) {
        self.edit_mode = EditMode::Off;
        self.edit_points.clear();
        self.confirm_token_remove = None;
        self.reconnect_at = None;
        self.reconnect_attempts = 0;
        if !self.load_scene(&summary, None) {
            return;
        }
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        self.screen = Screen::Scene;
        self.load_characters();
        self.load_notes();
        let scene_id = self.current_scene_id();
        self.connect_table(&campaign.id, &scene_id);
    }

    fn load_characters(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        match self.get_json::<CharacterList>(&format!("/v1/campaigns/{}/characters", campaign.id)) {
            Ok(list) => {
                self.characters = list.items;
                let selected_exists = self
                    .selected_character
                    .as_ref()
                    .is_some_and(|id| self.characters.iter().any(|item| &item.id == id));
                if !selected_exists {
                    let first = self.characters.first().map(|item| item.id.clone());
                    if let Some(id) = first {
                        self.select_character(&id);
                    } else {
                        self.selected_character = None;
                    }
                }
            }
            Err(error) => self.error = error,
        }
    }

    fn select_character(&mut self, id: &str) {
        let character = match self.characters.iter().find(|item| item.id == id) {
            Some(character) => character.clone(),
            None => return,
        };
        self.selected_character = Some(character.id.clone());
        self.sheet_name = character.name.clone();
        self.sheet_class = character
            .data
            .get("class")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string();
        self.sheet_level = character
            .data
            .get("level")
            .and_then(Value::as_i64)
            .unwrap_or(1) as i32;
        self.sheet_hp = character
            .data
            .get("hp")
            .and_then(Value::as_i64)
            .unwrap_or(10) as i32;
        self.sheet_hp_max = character
            .data
            .get("max_hp")
            .and_then(Value::as_i64)
            .unwrap_or(10) as i32;
        self.sheet_str = sheet_ability(&character.data, "str");
        self.sheet_dex = sheet_ability(&character.data, "dex");
        self.sheet_con = sheet_ability(&character.data, "con");
        self.sheet_int = sheet_ability(&character.data, "int");
        self.sheet_wis = sheet_ability(&character.data, "wis");
        self.sheet_cha = sheet_ability(&character.data, "cha");
        self.sheet_inventory = character
            .data
            .get("inventory")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string();
        self.sheet_spells = character
            .data
            .get("spells")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string();
        self.save_profs.clear();
        for (key, _) in ABILITY_KEYS {
            if sheet_flag(&character.data, "saves", key) {
                self.save_profs.insert(key.to_string());
            }
        }
        self.skill_profs.clear();
        for (key, _, _) in SKILLS {
            if sheet_flag(&character.data, "skills", key) {
                self.skill_profs.insert(key.to_string());
            }
        }
        self.sheet_notes = character
            .data
            .get("notes")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string();
    }

    fn sheet_data(&self) -> Value {
        let mut saves = serde_json::Map::new();
        for (key, _) in ABILITY_KEYS {
            saves.insert(key.to_string(), Value::Bool(self.save_profs.contains(key)));
        }
        let mut skills = serde_json::Map::new();
        for (key, _, _) in SKILLS {
            skills.insert(key.to_string(), Value::Bool(self.skill_profs.contains(key)));
        }
        serde_json::json!({
            "class": self.sheet_class.trim(),
            "level": self.sheet_level.max(1),
            "hp": self.sheet_hp.max(0),
            "max_hp": self.sheet_hp_max.max(0),
            "abilities": {
                "str": self.sheet_str.clamp(1, 30),
                "dex": self.sheet_dex.clamp(1, 30),
                "con": self.sheet_con.clamp(1, 30),
                "int": self.sheet_int.clamp(1, 30),
                "wis": self.sheet_wis.clamp(1, 30),
                "cha": self.sheet_cha.clamp(1, 30),
            },
            "saves": saves,
            "skills": skills,
            "inventory": self.sheet_inventory.trim(),
            "spells": self.sheet_spells.trim(),
            "notes": self.sheet_notes.trim(),
        })
    }

    fn ability_score(&self, key: &str) -> i32 {
        match key {
            "str" => self.sheet_str,
            "dex" => self.sheet_dex,
            "con" => self.sheet_con,
            "int" => self.sheet_int,
            "wis" => self.sheet_wis,
            "cha" => self.sheet_cha,
            _ => 10,
        }
    }

    fn roll_check(&mut self, label: &str, score: i32, proficient: bool) {
        let mut bonus = ability_modifier(score);
        if proficient {
            bonus += proficiency_bonus(self.sheet_level);
        }
        let formula = check_formula(bonus);
        self.roll_formula = formula.clone();
        self.send_intent(serde_json::json!({
            "type": "roll",
            "formula": formula,
            "visibility": "table",
        }));
        self.notice = format!("бросок {label}: {formula}");
    }

    fn create_character(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        let name = if self.new_character_name.trim().is_empty() {
            "Новый герой"
        } else {
            self.new_character_name.trim()
        };
        let body = serde_json::json!({"name": name, "data": self.sheet_data()});
        match self
            .post_json::<Character>(&format!("/v1/campaigns/{}/characters", campaign.id), body)
        {
            Ok(character) => {
                self.new_character_name.clear();
                self.notice = format!("лист «{}» создан", character.name);
                self.load_characters();
                self.select_character(&character.id);
            }
            Err(error) => self.error = error,
        }
    }

    fn save_character(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        let id = match &self.selected_character {
            Some(id) => id.clone(),
            None => {
                self.create_character();
                return;
            }
        };
        let body = serde_json::json!({"name": self.sheet_name.trim(), "data": self.sheet_data()});
        match self.patch_json::<Character>(
            &format!("/v1/campaigns/{}/characters/{}", campaign.id, id),
            body,
        ) {
            Ok(character) => {
                self.notice = format!("лист «{}» сохранён", character.name);
                self.load_characters();
            }
            Err(error) => self.error = error,
        }
    }

    fn load_notes(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        let query = self.note_query.trim();
        let path = if query.is_empty() {
            format!("/v1/campaigns/{}/notes", campaign.id)
        } else {
            format!(
                "/v1/campaigns/{}/notes?q={}",
                campaign.id,
                query_encode(query)
            )
        };
        match self.get_json::<NoteList>(&path) {
            Ok(list) => {
                self.notes = list.items;
                let selected_exists = self
                    .selected_note
                    .as_ref()
                    .is_some_and(|id| self.notes.iter().any(|note| &note.id == id));
                if !selected_exists {
                    let first = self.notes.first().map(|note| note.id.clone());
                    if let Some(id) = first {
                        self.select_note(&id);
                    } else {
                        self.selected_note = None;
                        self.note_title.clear();
                        self.note_body.clear();
                    }
                }
            }
            Err(error) => self.error = error,
        }
    }

    fn select_note(&mut self, id: &str) {
        let note = match self.notes.iter().find(|note| note.id == id) {
            Some(note) => note.clone(),
            None => return,
        };
        self.selected_note = Some(note.id);
        self.note_title = note.title;
        self.note_body = note.body;
        self.note_private = note.visibility == "private";
    }

    fn create_note(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        let title = if self.new_note_title.trim().is_empty() {
            "Новая запись"
        } else {
            self.new_note_title.trim()
        };
        let visibility = if self.new_note_private {
            "private"
        } else {
            "shared"
        };
        let body = serde_json::json!({"title": title, "body": "", "visibility": visibility});
        match self.post_json::<Note>(&format!("/v1/campaigns/{}/notes", campaign.id), body) {
            Ok(note) => {
                self.new_note_title.clear();
                self.new_note_private = false;
                self.note_query.clear();
                self.load_notes();
                self.select_note(&note.id);
                self.notice = format!("запись «{}» создана", note.title);
            }
            Err(error) => self.error = error,
        }
    }

    fn save_note(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        let id = match &self.selected_note {
            Some(id) => id.clone(),
            None => {
                self.create_note();
                return;
            }
        };
        let visibility = if self.note_private {
            "private"
        } else {
            "shared"
        };
        let body = serde_json::json!({"title": self.note_title, "body": self.note_body, "visibility": visibility});
        match self.patch_json::<Note>(&format!("/v1/campaigns/{}/notes/{}", campaign.id, id), body)
        {
            Ok(note) => {
                self.notice = format!("запись «{}» сохранена", note.title);
                self.note_query.clear();
                self.load_notes();
                self.select_note(&note.id);
            }
            Err(error) => self.error = error,
        }
    }

    fn delete_note(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        let id = match &self.selected_note {
            Some(id) => id.clone(),
            None => return,
        };
        match self.delete_request(&format!("/v1/campaigns/{}/notes/{}", campaign.id, id)) {
            Ok(()) => {
                self.selected_note = None;
                self.load_notes();
                self.notice = "запись удалена".to_string();
            }
            Err(error) => self.error = error,
        }
    }

    fn load_scene(&mut self, summary: &SceneSummary, tokens: Option<Vec<Token>>) -> bool {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return false,
        };
        self.error.clear();
        let scene: Scene = match self.get_json(&format!(
            "/v1/campaigns/{}/scenes/{}",
            campaign.id, summary.id
        )) {
            Ok(scene) => scene,
            Err(error) => {
                self.error = error;
                return false;
            }
        };
        match self.fetch_image(&campaign.id, &scene.image) {
            Ok(data) => {
                self.pending_image = Some(data);
                self.zoom = 0.0;
                self.offset = egui::Vec2::ZERO;
                self.scene = Some(scene);
                if let Some(tokens) = tokens {
                    self.tokens = tokens
                        .into_iter()
                        .filter(|token| token.scene_id == summary.id)
                        .collect();
                }
                self.screen = Screen::Scene;
                true
            }
            Err(error) => {
                self.error = error;
                false
            }
        }
    }

    fn fetch_image(&self, campaign_id: &str, image: &ImageInfo) -> Result<Vec<u8>, String> {
        let token = self.token.as_deref().unwrap_or("");
        let response = ureq::get(&format!(
            "{}/v1/campaigns/{}/images/{}",
            self.base_url, campaign_id, image.id
        ))
        .set("Authorization", &format!("Bearer {token}"))
        .call()
        .map_err(format_error)?;
        let mut data = Vec::new();
        response
            .into_reader()
            .read_to_end(&mut data)
            .map_err(|error| error.to_string())?;
        Ok(data)
    }

    fn make_fog_texture(&mut self, ctx: &egui::Context) {
        let mask = match self.pending_visibility.take() {
            Some(mask) => mask,
            None => return,
        };
        let blocks = mask.blocks as usize;
        if blocks == 0 {
            return;
        }
        let data = match base64::engine::general_purpose::STANDARD.decode(&mask.visible) {
            Ok(data) => data,
            Err(error) => {
                self.error = format!("маска видимости: {error}");
                return;
            }
        };
        if data.len() * 8 < blocks * blocks {
            self.error = "маска видимости короче карты".to_string();
            return;
        }
        let explored = if mask.explored.is_empty() {
            vec![0u8; data.len()]
        } else {
            match base64::engine::general_purpose::STANDARD.decode(&mask.explored) {
                Ok(data) if data.len() * 8 >= blocks * blocks => data,
                _ => vec![0u8; data.len()],
            }
        };
        let mut rgba = image::RgbaImage::new(blocks as u32, blocks as u32);
        for y in 0..blocks {
            for x in 0..blocks {
                let bit = y * blocks + x;
                let visible = data[bit / 8] & (1 << (bit % 8)) != 0;
                let seen = explored[bit / 8] & (1 << (bit % 8)) != 0;
                let pixel = if visible {
                    image::Rgba([0, 0, 0, 0])
                } else if seen {
                    image::Rgba([25, 18, 13, 118])
                } else {
                    image::Rgba([25, 18, 13, 238])
                };
                rgba.put_pixel(x as u32, y as u32, pixel);
            }
        }
        let size = [rgba.width() as usize, rgba.height() as usize];
        let color = egui::ColorImage::from_rgba_unmultiplied(size, rgba.as_raw());
        self.fog_texture = Some(ctx.load_texture("fog", color, egui::TextureOptions::LINEAR));
    }

    fn make_texture(&mut self, ctx: &egui::Context) {
        let data = match self.pending_image.take() {
            Some(data) => data,
            None => return,
        };
        let decoded = match image::load_from_memory(&data) {
            Ok(image) => image,
            Err(error) => {
                self.error = error.to_string();
                return;
            }
        };
        let rgba = decoded.to_rgba8();
        let size = [rgba.width() as usize, rgba.height() as usize];
        let color = egui::ColorImage::from_rgba_unmultiplied(size, rgba.as_raw());
        self.texture = Some(ctx.load_texture("map", color, egui::TextureOptions::LINEAR));
    }
}

fn format_error(error: ureq::Error) -> String {
    match error {
        ureq::Error::Status(code, response) => {
            let text = response.into_string().unwrap_or_default();
            format!("сервер ответил {code}: {text}")
        }
        ureq::Error::Transport(transport) => transport.to_string(),
    }
}

impl App {
    fn show_login(&mut self, ui: &mut egui::Ui) {
        ui.add_space(50.0);
        ui.vertical_centered(|ui| {
            ui.label(
                egui::RichText::new("VTT")
                    .size(42.0)
                    .strong()
                    .color(WARM_ACCENT),
            );
            ui.label(
                egui::RichText::new("стол, карта и кости")
                    .size(18.0)
                    .color(WARM_MUTED),
            );
            ui.add_space(18.0);
            warm_frame(WARM_PANEL).show(ui, |ui| {
                ui.set_width(420.0);
                ui.label("сервер");
                ui.add_sized(
                    [420.0, 38.0],
                    egui::TextEdit::singleline(&mut self.base_url),
                );
                ui.label("логин");
                ui.add_sized([420.0, 38.0], egui::TextEdit::singleline(&mut self.login));
                ui.label("пароль");
                ui.add_sized(
                    [420.0, 38.0],
                    egui::TextEdit::singleline(&mut self.password).password(true),
                );
                ui.add_space(12.0);
                if accent_button(ui, "Войти", 420.0).clicked() {
                    self.login();
                }
                if !self.error.is_empty() {
                    ui.colored_label(WARM_DANGER, &self.error);
                }
            });
        });
    }

    fn show_campaigns(&mut self, ui: &mut egui::Ui) {
        ui.add_space(6.0);
        let mut switch_archive = false;
        let mut requested_action = None;
        ui.horizontal(|ui| {
            ui.label(
                egui::RichText::new(if self.show_archived {
                    "Архив кампаний"
                } else {
                    "Мои кампании"
                })
                .size(30.0)
                .strong()
                .color(WARM_ACCENT),
            );
            ui.weak(format!("· {}", self.campaigns.len()));
            ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                if ui
                    .button(if self.show_archived {
                        "Активные"
                    } else {
                        "Архив"
                    })
                    .clicked()
                {
                    switch_archive = true;
                }
                if !self.show_archived {
                    if ui.button("Войти по коду").clicked() {
                        requested_action = Some(CampaignAction::Join);
                    }
                    if ui.button("Создать").clicked() {
                        requested_action = Some(CampaignAction::Create);
                    }
                }
            });
        });
        if let Some(action) = requested_action {
            self.campaign_action = if self.campaign_action == action {
                CampaignAction::None
            } else {
                action
            };
        }
        if switch_archive {
            self.show_archived = !self.show_archived;
            self.campaign_action = CampaignAction::None;
            if let Err(error) = self.load_campaigns() {
                self.error = error;
            }
            return;
        }
        ui.label(
            egui::RichText::new(if self.show_archived {
                "кампании, убранные из основного списка"
            } else {
                "столы, где вы участник"
            })
            .size(15.0)
            .color(WARM_MUTED),
        );
        ui.add_space(10.0);

        let width = ui.available_width().min(820.0);
        if self.campaign_action == CampaignAction::Create && !self.show_archived {
            warm_frame(WARM_PANEL).show(ui, |ui| {
                ui.set_width(width);
                ui.label(
                    egui::RichText::new("Новая кампания")
                        .size(19.0)
                        .strong()
                        .color(WARM_TEXT),
                );
                ui.horizontal(|ui| {
                    ui.add_sized(
                        [320.0, 40.0],
                        egui::TextEdit::singleline(&mut self.new_campaign).hint_text("название"),
                    );
                    if accent_button(ui, "Создать", 130.0).clicked() {
                        self.create_campaign();
                    }
                    if ui
                        .add_sized(
                            [140.0, 42.0],
                            egui::Button::new(egui::RichText::new("Демо-стол").strong())
                                .fill(WARM_ACCENT_DARK)
                                .stroke(egui::Stroke::new(1.5, WARM_ACCENT)),
                        )
                        .clicked()
                    {
                        self.create_demo();
                    }
                });
            });
            ui.add_space(10.0);
        }
        if self.campaign_action == CampaignAction::Join && !self.show_archived {
            warm_frame(WARM_PANEL).show(ui, |ui| {
                ui.set_width(width);
                ui.label(
                    egui::RichText::new("Войти по коду")
                        .size(19.0)
                        .strong()
                        .color(WARM_TEXT),
                );
                ui.label(
                    egui::RichText::new("код приглашения выдаёт мастер кампании")
                        .size(14.0)
                        .color(WARM_MUTED),
                );
                ui.horizontal(|ui| {
                    ui.add_sized(
                        [240.0, 40.0],
                        egui::TextEdit::singleline(&mut self.join_code).hint_text("код от мастера"),
                    );
                    if accent_button(ui, "Войти", 130.0).clicked() {
                        self.accept_invite();
                    }
                });
            });
            ui.add_space(10.0);
        }

        if self.campaigns.is_empty() {
            warm_frame(WARM_CARD).show(ui, |ui| {
                ui.set_width(width);
                ui.label(
                    egui::RichText::new(if self.show_archived {
                        "архив пуст"
                    } else {
                        "вы пока не участвуете в кампаниях"
                    })
                    .size(17.0)
                    .color(WARM_MUTED),
                );
                if !self.show_archived {
                    ui.label(
                        egui::RichText::new("создайте кампанию или войдите по коду")
                            .size(14.0)
                            .color(WARM_MUTED),
                    );
                }
            });
        }
        let items = self.campaigns.clone();
        for campaign in items {
            let role_color = match campaign.role.as_str() {
                "owner" => WARM_ACCENT,
                "master" => WARM_ACCENT_HOVER,
                "player" => WARM_TEXT,
                _ => WARM_MUTED,
            };
            let mut open = false;
            let mut restore = false;
            warm_frame(WARM_CARD).show(ui, |ui| {
                ui.set_width(width);
                ui.horizontal(|ui| {
                    ui.label(
                        egui::RichText::new(&campaign.name)
                            .size(20.0)
                            .strong()
                            .color(WARM_TEXT),
                    );
                    ui.label(
                        egui::RichText::new(role_name(&campaign.role))
                            .size(14.0)
                            .color(role_color),
                    );
                    if campaign.archived {
                        ui.weak("в архиве");
                    }
                    ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                        if self.show_archived {
                            if campaign.role == "owner" {
                                if accent_button(ui, "Вернуть", 130.0).clicked() {
                                    restore = true;
                                }
                            }
                        } else if accent_button(ui, "Открыть →", 150.0).clicked() {
                            open = true;
                        }
                    });
                });
            });
            if open {
                self.open_campaign(campaign);
                return;
            }
            if restore {
                self.unarchive_campaign(&campaign);
                return;
            }
            ui.add_space(6.0);
        }
    }

    fn show_scenes(&mut self, ui: &mut egui::Ui) {
        if ui
            .add_sized([130.0, 38.0], egui::Button::new("← Кампании"))
            .clicked()
        {
            self.screen = Screen::Campaigns;
        }
        ui.add_space(4.0);
        let width = ui.available_width().min(760.0);
        if let Some(campaign) = &self.campaign {
            ui.label(
                egui::RichText::new(&campaign.name)
                    .size(30.0)
                    .strong()
                    .color(WARM_ACCENT),
            );
            if campaign.role == "owner" {
                if accent_button(ui, "Пригласить игрока", 230.0).clicked() {
                    self.create_invite();
                }
                if ui.button("В архив").clicked() {
                    self.archive_campaign();
                }
                if !self.invite_code.is_empty() {
                    ui.label(
                        egui::RichText::new(format!("код приглашения: {}", self.invite_code))
                            .size(18.0)
                            .strong()
                            .color(WARM_ACCENT_HOVER),
                    );
                }
            }
        }
        let members = self
            .campaign_detail
            .as_ref()
            .map(|detail| detail.members.clone())
            .unwrap_or_default();
        let is_owner = self
            .campaign
            .as_ref()
            .map(|campaign| campaign.role == "owner")
            .unwrap_or(false);
        if !members.is_empty() {
            let mut role_change = None;
            let mut remove = None;
            warm_frame(WARM_PANEL).show(ui, |ui| {
                ui.set_width(width);
                ui.label(
                    egui::RichText::new("Участники")
                        .size(18.0)
                        .strong()
                        .color(WARM_TEXT),
                );
                for member in &members {
                    ui.horizontal(|ui| {
                        ui.label(&member.login);
                        if is_owner && member.role != "owner" {
                            let mut role = member.role.clone();
                            egui::ComboBox::from_id_salt(format!("member-role-{}", member.id))
                                .selected_text(role_name(&member.role))
                                .show_ui(ui, |ui| {
                                    ui.selectable_value(&mut role, "master".to_string(), "мастер");
                                    ui.selectable_value(&mut role, "player".to_string(), "игрок");
                                    ui.selectable_value(
                                        &mut role,
                                        "observer".to_string(),
                                        "наблюдатель",
                                    );
                                });
                            if role != member.role {
                                role_change = Some((member.id.clone(), role));
                            }
                            if ui.small_button("убрать").clicked() {
                                remove = Some(member.id.clone());
                            }
                            if ui.small_button("владелец").clicked() {
                                self.confirm_owner = Some(member.id.clone());
                            }
                        } else {
                            ui.weak(role_name(&member.role));
                        }
                    });
                }
            });
            if let Some((id, role)) = role_change {
                self.change_member_role(&id, &role);
            }
            if let Some(id) = remove {
                self.remove_member(&id);
            }
            if let Some(id) = self.confirm_owner.clone() {
                let login = members
                    .iter()
                    .find(|member| member.id == id)
                    .map(|member| member.login.clone())
                    .unwrap_or_else(|| "участник".to_string());
                ui.horizontal(|ui| {
                    ui.label(
                        egui::RichText::new(format!("Передать владение {login}?"))
                            .color(WARM_DANGER),
                    );
                    if accent_button(ui, "Да", 80.0).clicked() {
                        self.transfer_ownership(&id);
                    }
                    if ui.button("Отмена").clicked() {
                        self.confirm_owner = None;
                    }
                });
            }
        }
        ui.add_space(10.0);
        ui.label(
            egui::RichText::new("Сцены")
                .size(22.0)
                .strong()
                .color(WARM_TEXT),
        );
        if self.scenes.is_empty() {
            ui.label(if self.is_master() {
                "сцен нет — откройте «Импорт карты» ниже"
            } else {
                "мастер ещё не показал сцену"
            });
        } else if !self.is_master() {
            ui.label(
                egui::RichText::new("сцену показывает мастер")
                    .size(14.0)
                    .color(WARM_MUTED),
            );
        }
        let items = self.scenes.clone();
        for scene in items {
            if !self.is_master() && !scene.active {
                continue;
            }
            let subtitle = if scene.active {
                "● показана мастером — открыть"
            } else {
                "открыть стол"
            };
            if card_button(ui, &scene.name, subtitle, width).clicked() {
                self.open_scene(scene);
            }
            ui.add_space(6.0);
        }
        if let Some(campaign) = &self.campaign {
            if campaign.role == "owner" || campaign.role == "master" {
                ui.add_space(10.0);
                egui::CollapsingHeader::new(
                    egui::RichText::new("Импорт карты").size(18.0).strong(),
                )
                .default_open(self.scenes.is_empty())
                .show(ui, |ui| {
                    ui.label("перетащите в окно картинку и annotation.json — или впишите пути");
                    ui.label(format!("картинка: {}", file_name(&self.image_path)));
                    ui.add_sized(
                        [width, 38.0],
                        egui::TextEdit::singleline(&mut self.image_path),
                    );
                    ui.label(format!(
                        "annotation.json: {}",
                        file_name(&self.annotation_path)
                    ));
                    ui.add_sized(
                        [width, 38.0],
                        egui::TextEdit::singleline(&mut self.annotation_path),
                    );
                    ui.label("имя сцены");
                    ui.add_sized(
                        [width, 38.0],
                        egui::TextEdit::singleline(&mut self.scene_name),
                    );
                    if accent_button(ui, "Импортировать", 220.0).clicked() {
                        self.import_scene();
                    }
                });
            }
        }
    }
}

impl App {
    fn show_scene(&mut self, ui: &mut egui::Ui) {
        let mut show_all = false;
        ui.horizontal(|ui| {
            if ui
                .add_sized([110.0, 38.0], egui::Button::new("← Сцены"))
                .clicked()
            {
                self.screen = Screen::Scenes;
            }
            if let Some(scene) = &self.scene {
                ui.label(
                    egui::RichText::new(&scene.name)
                        .size(26.0)
                        .strong()
                        .color(WARM_ACCENT),
                );
            }
            if let Some(campaign) = &self.campaign {
                if campaign.role == "owner" || campaign.role == "master" {
                    if accent_button(ui, "Показать всем", 180.0).clicked() {
                        show_all = true;
                    }
                }
            }
        });
        if show_all {
            let scene_id = self.current_scene_id();
            self.send_intent(serde_json::json!({
                "type": "scene.show",
                "scene_id": scene_id,
            }));
        }
        if self.is_master() {
            let light = self
                .scene
                .as_ref()
                .map(|scene| scene.light.as_str())
                .unwrap_or("bright");
            let (label, next) = if light == "dark" {
                ("Свет: темно", "bright")
            } else {
                ("Свет: ярко", "dark")
            };
            if accent_button(ui, label, 170.0).clicked() {
                self.send_intent(serde_json::json!({"type": "scene.light", "light": next}));
            }
        }
        if self.is_master() {
            ui.horizontal(|ui| {
                if self.edit_mode == EditMode::Off {
                    if accent_button(ui, "Редактор сцены", 190.0).clicked() {
                        self.edit_mode = EditMode::Wall;
                        self.edit_points.clear();
                    }
                } else {
                    if ui
                        .selectable_label(self.edit_mode == EditMode::Wall, "Стена")
                        .clicked()
                    {
                        self.edit_mode = EditMode::Wall;
                        self.edit_points.clear();
                    }
                    if ui
                        .selectable_label(self.edit_mode == EditMode::Door, "Дверь")
                        .clicked()
                    {
                        self.edit_mode = EditMode::Door;
                        self.edit_points.clear();
                    }
                    if self.edit_mode == EditMode::Wall
                        && accent_button(ui, "Завершить стену", 180.0).clicked()
                    {
                        self.finish_wall();
                    }
                    if accent_button(ui, "Сохранить", 130.0).clicked() {
                        self.save_scene_annotation();
                    }
                    if ui.button("Отмена").clicked() {
                        self.edit_mode = EditMode::Off;
                        self.edit_points.clear();
                    }
                }
            });
            if self.edit_mode != EditMode::Off {
                let hint = if self.edit_mode == EditMode::Wall {
                    "клик по карте — точка стены; «Завершить стену» закрывает линию"
                } else {
                    "клик по стене — поставить на неё дверь"
                };
                ui.label(egui::RichText::new(hint).color(WARM_MUTED).size(15.0));
            }
        }
        if !self.hint_dismissed {
            let text = if self.is_master() {
                "1. Вкладка «Мастер»: поставьте фишку и выберите владельца.  2. «Показать всем».  3. Вкладка «Бой»: начните бой."
            } else {
                "Перетащите свою фишку, бросьте кубы во вкладке «Чат» и дождитесь своего хода во вкладке «Бой»."
            };
            let mut dismiss = false;
            warm_frame(WARM_CARD).show(ui, |ui| {
                ui.horizontal(|ui| {
                    ui.label(egui::RichText::new(text).size(16.0));
                    if accent_button(ui, "Понятно", 110.0).clicked() {
                        dismiss = true;
                    }
                });
            });
            if dismiss {
                self.hint_dismissed = true;
            }
        }
        ui.separator();
        self.show_map(ui);
    }

    fn show_map(&mut self, ui: &mut egui::Ui) {
        let scene = match &self.scene {
            Some(scene) => scene.clone(),
            None => return,
        };
        let texture = match &self.texture {
            Some(texture) => texture.clone(),
            None => {
                ui.label("карта загружается…");
                return;
            }
        };
        let (response, painter) =
            ui.allocate_painter(ui.available_size(), egui::Sense::click_and_drag());
        let rect = response.rect;
        let image_w = scene.image.width as f32;
        let image_h = scene.image.height as f32;
        if self.zoom <= 0.0 {
            self.zoom = (rect.width() / image_w).min(rect.height() / image_h) * 0.98;
        }
        let scroll = ui.input(|input| input.smooth_scroll_delta.y);
        if scroll.abs() > 0.0 {
            let factor = if scroll > 0.0 { 1.1 } else { 1.0 / 1.1 };
            self.zoom = (self.zoom * factor).clamp(0.02, 12.0);
        }
        let size = egui::vec2(image_w * self.zoom, image_h * self.zoom);
        let origin = rect.center() - size / 2.0 + self.offset;
        let map_rect = egui::Rect::from_min_size(origin, size);
        let pointer = ui.input(|input| input.pointer.interact_pos());
        if self.edit_mode != EditMode::Off && response.clicked() {
            if let Some(position) = pointer {
                if map_rect.contains(position) {
                    let x = ((position.x - origin.x) / self.zoom).clamp(0.0, image_w);
                    let y = ((position.y - origin.y) / self.zoom).clamp(0.0, image_h);
                    match self.edit_mode {
                        EditMode::Wall => self.edit_points.push((x, y)),
                        EditMode::Door => self.add_door_at(x, y),
                        EditMode::Off => {}
                    }
                }
            }
        }
        if self.edit_mode == EditMode::Off && response.drag_started() {
            if let Some(position) = pointer {
                self.drag_token = self
                    .tokens
                    .iter()
                    .find(|token| {
                        let center = egui::pos2(
                            origin.x + token.x as f32 * self.zoom,
                            origin.y + token.y as f32 * self.zoom,
                        );
                        center.distance(position) <= 18.0 && self.can_move_token(token)
                    })
                    .map(|token| token.id.clone());
            }
        }
        if self.drag_token.is_some() {
            if response.dragged() {
                if let Some(position) = pointer {
                    let x = ((position.x - origin.x) / self.zoom).clamp(0.0, image_w) as f64;
                    let y = ((position.y - origin.y) / self.zoom).clamp(0.0, image_h) as f64;
                    if let Some(token) = self
                        .tokens
                        .iter_mut()
                        .find(|token| Some(&token.id) == self.drag_token.as_ref())
                    {
                        token.x = x;
                        token.y = y;
                    }
                }
            }
            if response.drag_stopped() {
                if let Some(id) = self.drag_token.take() {
                    if let Some(token) = self.tokens.iter().find(|token| token.id == id) {
                        self.move_token(&id, token.x, token.y);
                    }
                }
            }
        } else if response.dragged() {
            self.offset += response.drag_delta();
        }
        painter.image(
            texture.id(),
            map_rect,
            egui::Rect::from_min_max(egui::pos2(0.0, 0.0), egui::pos2(1.0, 1.0)),
            egui::Color32::WHITE,
        );

        let to_screen =
            |x: f32, y: f32| egui::pos2(origin.x + x * self.zoom, origin.y + y * self.zoom);
        let stroke_yellow = egui::Stroke::new(2.0, egui::Color32::from_rgb(255, 205, 60));
        let stroke_green = egui::Stroke::new(3.0, egui::Color32::from_rgb(70, 210, 110));
        let stroke_blue = egui::Stroke::new(2.0, egui::Color32::from_rgb(80, 150, 255));
        let stroke_grid = egui::Stroke::new(
            1.0,
            egui::Color32::from_rgba_unmultiplied(220, 220, 255, 45),
        );

        if scene.grid.get("mode").and_then(Value::as_str) == Some("square") {
            if let (Some(origin), Some(side)) = (
                scene.grid.get("origin").and_then(Value::as_array),
                scene
                    .grid
                    .pointer("/square/side_px")
                    .and_then(Value::as_f64),
            ) {
                if origin.len() == 2 {
                    let ox = origin[0].as_f64().unwrap_or(0.0) as f32 - side as f32 / 2.0;
                    let oy = origin[1].as_f64().unwrap_or(0.0) as f32 - side as f32 / 2.0;
                    let mut x = ox;
                    while x < image_w {
                        painter
                            .line_segment([to_screen(x, 0.0), to_screen(x, image_h)], stroke_grid);
                        x += side as f32;
                    }
                    let mut y = oy;
                    while y < image_h {
                        painter
                            .line_segment([to_screen(0.0, y), to_screen(image_w, y)], stroke_grid);
                        y += side as f32;
                    }
                }
            }
        }

        let points_of = |value: &Value| -> Vec<egui::Pos2> {
            value
                .as_array()
                .map(|points| {
                    points
                        .iter()
                        .filter_map(|point| {
                            let pair = point.as_array()?;
                            Some(to_screen(
                                pair.first()?.as_f64()? as f32,
                                pair.get(1)?.as_f64()? as f32,
                            ))
                        })
                        .collect()
                })
                .unwrap_or_default()
        };

        for wall in scene.walls.as_array().into_iter().flatten() {
            if let Some(points) = wall.get("points") {
                let line = points_of(points);
                for pair in line.windows(2) {
                    painter.line_segment([pair[0], pair[1]], stroke_yellow);
                }
            }
        }
        for door in scene.doors.as_array().into_iter().flatten() {
            let from = door.get("from").map(points_of).unwrap_or_default();
            let to = door.get("to").map(points_of).unwrap_or_default();
            if let (Some(from), Some(to)) = (from.first(), to.first()) {
                let open = door
                    .get("id")
                    .and_then(Value::as_str)
                    .and_then(|id| self.doors.get(id))
                    .copied()
                    .unwrap_or(false);
                let stroke = if open {
                    egui::Stroke::new(1.5, egui::Color32::from_rgb(150, 150, 150))
                } else {
                    stroke_green
                };
                painter.line_segment([*from, *to], stroke);
            }
        }
        for area in scene.areas.as_array().into_iter().flatten() {
            if let Some(points) = area.get("polygon") {
                let line = points_of(points);
                for pair in line.windows(2) {
                    painter.line_segment([pair[0], pair[1]], stroke_blue);
                }
            }
        }
        if self.edit_mode == EditMode::Wall && !self.edit_points.is_empty() {
            let preview: Vec<egui::Pos2> = self
                .edit_points
                .iter()
                .map(|(x, y)| to_screen(*x, *y))
                .collect();
            for pair in preview.windows(2) {
                painter.line_segment([pair[0], pair[1]], stroke_blue);
            }
            for point in preview {
                painter.circle_filled(point, 5.0, WARM_ACCENT);
            }
        }
        for token in &self.tokens {
            let center = to_screen(token.x as f32, token.y as f32);
            let fill = token_color(&token.color);
            let radius = 15.0;
            painter.circle_filled(center, radius, fill);
            painter.circle_stroke(
                center,
                radius,
                egui::Stroke::new(2.0, egui::Color32::from_rgb(30, 30, 30)),
            );
            let label = center + egui::vec2(0.0, radius + 10.0);
            painter.text(
                label + egui::vec2(1.0, 1.0),
                egui::Align2::CENTER_CENTER,
                &token.name,
                egui::FontId::proportional(14.0),
                egui::Color32::BLACK,
            );
            painter.text(
                label,
                egui::Align2::CENTER_CENTER,
                &token.name,
                egui::FontId::proportional(14.0),
                egui::Color32::WHITE,
            );
        }
        if !self.is_master() || self.preview_name.is_some() {
            if let Some(fog) = &self.fog_texture {
                painter.image(
                    fog.id(),
                    map_rect,
                    egui::Rect::from_min_max(egui::pos2(0.0, 0.0), egui::pos2(1.0, 1.0)),
                    egui::Color32::WHITE,
                );
            }
        }
    }
}

fn fill_rect(
    canvas: &mut image::RgbaImage,
    x0: u32,
    y0: u32,
    x1: u32,
    y1: u32,
    color: image::Rgba<u8>,
) {
    for y in y0..y1.min(canvas.height()) {
        for x in x0..x1.min(canvas.width()) {
            canvas.put_pixel(x, y, color);
        }
    }
}

fn hline(
    canvas: &mut image::RgbaImage,
    x0: u32,
    x1: u32,
    y: u32,
    thickness: u32,
    color: image::Rgba<u8>,
) {
    fill_rect(
        canvas,
        x0,
        y.saturating_sub(thickness / 2),
        x1,
        y + thickness / 2 + 1,
        color,
    );
}

fn vline(
    canvas: &mut image::RgbaImage,
    x: u32,
    y0: u32,
    y1: u32,
    thickness: u32,
    color: image::Rgba<u8>,
) {
    fill_rect(
        canvas,
        x.saturating_sub(thickness / 2),
        y0,
        x + thickness / 2 + 1,
        y1,
        color,
    );
}

// demo_scene рисует маленькую таверну и отдаёт пару «картинка + annotation.json»,
// чтобы демо-стол не требовал файлов на диске.
fn demo_scene() -> (Vec<u8>, Vec<u8>) {
    const W: u32 = 800;
    const H: u32 = 600;
    let mut canvas = image::RgbaImage::from_pixel(W, H, image::Rgba([22, 22, 28, 255]));
    let floor = image::Rgba([64, 57, 51, 255]);
    let room = image::Rgba([78, 68, 60, 255]);
    let grid = image::Rgba([92, 86, 100, 255]);
    let wall = image::Rgba([220, 190, 90, 255]);
    let door = image::Rgba([170, 105, 55, 255]);

    fill_rect(&mut canvas, 50, 50, 750, 550, floor);
    fill_rect(&mut canvas, 60, 60, 390, 290, room);
    fill_rect(&mut canvas, 410, 60, 740, 290, room);
    fill_rect(&mut canvas, 60, 310, 740, 540, room);

    let mut x = 50;
    while x <= 750 {
        vline(&mut canvas, x, 50, 550, 1, grid);
        x += 50;
    }
    let mut y = 50;
    while y <= 550 {
        hline(&mut canvas, 50, 750, y, 1, grid);
        y += 50;
    }

    for (x0, y0, x1, y1) in [
        (50, 50, 750, 50),
        (750, 50, 750, 550),
        (750, 550, 50, 550),
        (50, 550, 50, 50),
        (400, 50, 400, 550),
    ] {
        if x0 == x1 {
            vline(&mut canvas, x0, y0.min(y1), y0.max(y1), 7, wall);
        } else {
            hline(&mut canvas, x0.min(x1), x0.max(x1), y0, 7, wall);
        }
    }
    vline(&mut canvas, 400, 250, 330, 12, door);
    hline(&mut canvas, 50, 70, 50, 7, door);

    let mut png = Vec::new();
    image::codecs::png::PngEncoder::new(&mut png)
        .write_image(canvas.as_raw(), W, H, image::ExtendedColorType::Rgba8)
        .expect("демо-карта кодируется в PNG");

    let annotation = serde_json::json!({
        "format_version": 1,
        "image": {"width": W, "height": H},
        "grid": {"mode": "square", "origin": [0, 0], "square": {"side_px": 50}},
        "walls": [
            {"id": "w1", "points": [[50, 50], [750, 50]]},
            {"id": "w2", "points": [[750, 50], [750, 550]]},
            {"id": "w3", "points": [[750, 550], [50, 550]]},
            {"id": "w4", "points": [[50, 550], [50, 50]]},
            {"id": "w5", "points": [[400, 50], [400, 550]]}
        ],
        "doors": [
            {"id": "d1", "wall_id": "w5", "type": "normal", "from": [400, 250], "to": [400, 330]}
        ],
        "areas": []
    });
    (png, serde_json::to_vec(&annotation).expect("демо-разметка"))
}

fn file_name(path: &str) -> String {
    if path.trim().is_empty() {
        return "не выбран".to_string();
    }
    std::path::Path::new(path)
        .file_name()
        .and_then(|name| name.to_str())
        .map(str::to_string)
        .unwrap_or_else(|| path.to_string())
}

fn role_name(role: &str) -> &'static str {
    match role {
        "owner" => "владелец",
        "master" => "мастер",
        "player" => "игрок",
        "observer" => "наблюдатель",
        _ => "участник",
    }
}

const WARM_BG: egui::Color32 = egui::Color32::from_rgb(34, 27, 22);
const WARM_PANEL: egui::Color32 = egui::Color32::from_rgb(45, 35, 28);
const WARM_CARD: egui::Color32 = egui::Color32::from_rgb(58, 45, 36);
const WARM_CARD_HOVER: egui::Color32 = egui::Color32::from_rgb(74, 56, 43);
const WARM_BORDER: egui::Color32 = egui::Color32::from_rgb(104, 78, 58);
const WARM_TEXT: egui::Color32 = egui::Color32::from_rgb(245, 231, 216);
const WARM_MUTED: egui::Color32 = egui::Color32::from_rgb(201, 173, 148);
const WARM_ACCENT: egui::Color32 = egui::Color32::from_rgb(217, 154, 78);
const WARM_ACCENT_HOVER: egui::Color32 = egui::Color32::from_rgb(235, 177, 104);
const WARM_ACCENT_DARK: egui::Color32 = egui::Color32::from_rgb(147, 94, 45);
const WARM_DANGER: egui::Color32 = egui::Color32::from_rgb(224, 108, 90);

fn apply_theme(ctx: &egui::Context) {
    let mut style = (*ctx.global_style()).clone();
    let mut visuals = egui::Visuals::dark();
    visuals.panel_fill = WARM_BG;
    visuals.window_fill = WARM_PANEL;
    visuals.extreme_bg_color = egui::Color32::from_rgb(27, 21, 17);
    visuals.faint_bg_color = WARM_PANEL;
    visuals.override_text_color = Some(WARM_TEXT);
    visuals.hyperlink_color = WARM_ACCENT;
    visuals.warn_fg_color = WARM_ACCENT_HOVER;
    visuals.error_fg_color = WARM_DANGER;
    visuals.selection.bg_fill = WARM_ACCENT_DARK;
    visuals.selection.stroke = egui::Stroke::new(1.5, WARM_TEXT);

    let widgets = &mut visuals.widgets;
    widgets.noninteractive.bg_fill = WARM_PANEL;
    widgets.noninteractive.bg_stroke = egui::Stroke::new(1.0, WARM_BORDER);
    widgets.noninteractive.fg_stroke = egui::Stroke::new(1.0, WARM_MUTED);
    widgets.noninteractive.corner_radius = egui::CornerRadius::same(8);
    widgets.inactive.bg_fill = WARM_CARD;
    widgets.inactive.weak_bg_fill = WARM_CARD;
    widgets.inactive.bg_stroke = egui::Stroke::new(1.0, WARM_BORDER);
    widgets.inactive.fg_stroke = egui::Stroke::new(1.0, WARM_TEXT);
    widgets.inactive.corner_radius = egui::CornerRadius::same(8);
    widgets.hovered.bg_fill = WARM_CARD_HOVER;
    widgets.hovered.weak_bg_fill = WARM_CARD_HOVER;
    widgets.hovered.bg_stroke = egui::Stroke::new(1.5, WARM_ACCENT);
    widgets.hovered.fg_stroke = egui::Stroke::new(1.5, WARM_ACCENT_HOVER);
    widgets.hovered.corner_radius = egui::CornerRadius::same(8);
    widgets.active.bg_fill = WARM_ACCENT_DARK;
    widgets.active.weak_bg_fill = WARM_ACCENT_DARK;
    widgets.active.bg_stroke = egui::Stroke::new(1.5, WARM_ACCENT_HOVER);
    widgets.active.fg_stroke = egui::Stroke::new(1.5, WARM_TEXT);
    widgets.active.corner_radius = egui::CornerRadius::same(8);
    widgets.open.bg_fill = WARM_CARD_HOVER;
    widgets.open.weak_bg_fill = WARM_CARD_HOVER;
    widgets.open.bg_stroke = egui::Stroke::new(1.5, WARM_ACCENT);
    widgets.open.fg_stroke = egui::Stroke::new(1.5, WARM_ACCENT_HOVER);
    widgets.open.corner_radius = egui::CornerRadius::same(8);
    visuals.window_corner_radius = egui::CornerRadius::same(12);

    style.visuals = visuals;
    style.text_styles = [
        (egui::TextStyle::Heading, egui::FontId::proportional(28.0)),
        (egui::TextStyle::Body, egui::FontId::proportional(17.0)),
        (egui::TextStyle::Monospace, egui::FontId::monospace(15.0)),
        (egui::TextStyle::Button, egui::FontId::proportional(18.0)),
        (egui::TextStyle::Small, egui::FontId::proportional(14.0)),
    ]
    .into();
    style.spacing.item_spacing = egui::vec2(10.0, 8.0);
    style.spacing.button_padding = egui::vec2(16.0, 10.0);
    style.spacing.interact_size = egui::vec2(48.0, 38.0);
    style.spacing.slider_width = 140.0;
    ctx.set_global_style(style);
}

fn warm_frame(fill: egui::Color32) -> egui::Frame {
    egui::Frame::new()
        .fill(fill)
        .stroke(egui::Stroke::new(1.0, WARM_BORDER))
        .corner_radius(egui::CornerRadius::same(12))
        .inner_margin(egui::Margin::same(14))
}

fn accent_button(ui: &mut egui::Ui, text: &str, width: f32) -> egui::Response {
    let button = egui::Button::new(egui::RichText::new(text).strong())
        .fill(WARM_ACCENT_DARK)
        .stroke(egui::Stroke::new(1.5, WARM_ACCENT));
    ui.add_sized([width, 42.0], button)
}

fn card_button(ui: &mut egui::Ui, title: &str, subtitle: &str, width: f32) -> egui::Response {
    let text = format!("{title}\n{subtitle}");
    let button = egui::Button::new(egui::RichText::new(text).size(18.0))
        .fill(WARM_CARD)
        .stroke(egui::Stroke::new(1.0, WARM_BORDER))
        .min_size(egui::vec2(width, 62.0));
    ui.add(button)
}

fn token_color(value: &str) -> egui::Color32 {
    let text = value.trim().trim_start_matches('#');
    if text.len() == 6 {
        if let (Ok(r), Ok(g), Ok(b)) = (
            u8::from_str_radix(&text[0..2], 16),
            u8::from_str_radix(&text[2..4], 16),
            u8::from_str_radix(&text[4..6], 16),
        ) {
            return egui::Color32::from_rgb(r, g, b);
        }
    }
    egui::Color32::from_rgb(232, 197, 71)
}

fn multipart_body(image: &[u8], annotation: &[u8], name: &str) -> (Vec<u8>, String) {
    let boundary = "----vtt-boundary-7f3a9c";
    let mut body = Vec::new();
    let mut field = |name: &str, filename: &str, content_type: &str, data: &[u8]| {
        body.extend_from_slice(format!("--{boundary}\r\n").as_bytes());
        body.extend_from_slice(
            format!("Content-Disposition: form-data; name=\"{name}\"").as_bytes(),
        );
        if !filename.is_empty() {
            body.extend_from_slice(format!("; filename=\"{filename}\"").as_bytes());
        }
        body.extend_from_slice(b"\r\n");
        if !content_type.is_empty() {
            body.extend_from_slice(format!("Content-Type: {content_type}\r\n").as_bytes());
        }
        body.extend_from_slice(b"\r\n");
        body.extend_from_slice(data);
        body.extend_from_slice(b"\r\n");
    };
    field("image", "map.png", "application/octet-stream", image);
    field(
        "annotation",
        "annotation.json",
        "application/json",
        annotation,
    );
    field("name", "", "", name.as_bytes());
    body.extend_from_slice(format!("--{boundary}--\r\n").as_bytes());
    (body, format!("multipart/form-data; boundary={boundary}"))
}

impl App {
    fn post_multipart(
        &self,
        path: &str,
        body: Vec<u8>,
        content_type: String,
    ) -> Result<Value, String> {
        let token = self.token.as_deref().unwrap_or("");
        ureq::post(&format!("{}{}", self.base_url, path))
            .set("Authorization", &format!("Bearer {token}"))
            .set("Content-Type", &content_type)
            .send_bytes(&body)
            .map_err(format_error)?
            .into_json::<Value>()
            .map_err(|error| error.to_string())
    }

    fn accept_dropped_files(&mut self, ctx: &egui::Context) {
        let files = ctx.input(|state| state.raw.dropped_files.clone());
        for file in files {
            let path = file.path().to_path_buf();
            let is_json = path
                .extension()
                .and_then(|ext| ext.to_str())
                .map(|ext| ext.eq_ignore_ascii_case("json"))
                .unwrap_or(false);
            let text = path.to_string_lossy().to_string();
            if is_json {
                self.annotation_path = text;
            } else {
                self.image_path = text;
            }
        }
        if !self.image_path.is_empty() || !self.annotation_path.is_empty() {
            self.notice =
                "файлы выбраны: проверьте имя сцены и нажмите «Импортировать»".to_string();
        }
    }

    fn import_scene(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        let image = match fs::read(self.image_path.trim()) {
            Ok(data) => data,
            Err(error) => {
                self.error = format!("картинка: {error}");
                return;
            }
        };
        let annotation = match fs::read(self.annotation_path.trim()) {
            Ok(data) => data,
            Err(error) => {
                self.error = format!("разметка: {error}");
                return;
            }
        };
        let name = if self.scene_name.trim().is_empty() {
            "Сцена".to_string()
        } else {
            self.scene_name.clone()
        };
        let (body, content_type) = multipart_body(&image, &annotation, &name);
        match self.post_multipart(
            &format!("/v1/campaigns/{}/scenes:import", campaign.id),
            body,
            content_type,
        ) {
            Ok(_) => {
                self.notice = "карта импортирована".to_string();
                self.open_campaign(campaign);
            }
            Err(error) => self.error = error,
        }
    }
    fn finish_wall(&mut self) {
        if self.edit_points.len() < 2 {
            self.notice = "стена: нужно хотя бы две точки".to_string();
            return;
        }
        let id = format!(
            "w{}",
            100000
                + std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .map(|value| value.as_millis() as u64 % 900000)
                    .unwrap_or(0)
        );
        let points: Vec<Value> = self
            .edit_points
            .iter()
            .map(|(x, y)| serde_json::json!([x.round() as i64, y.round() as i64]))
            .collect();
        if let Some(scene) = self.scene.as_mut() {
            if let Some(walls) = scene.walls.as_array_mut() {
                walls.push(serde_json::json!({"id": id, "points": points}));
            }
        }
        self.edit_points.clear();
        self.notice = "стена добавлена — не забудьте сохранить".to_string();
    }

    fn add_door_at(&mut self, x: f32, y: f32) {
        let scene = match self.scene.as_mut() {
            Some(scene) => scene,
            None => return,
        };
        let walls = match scene.walls.as_array() {
            Some(walls) => walls,
            None => return,
        };
        let mut best: Option<(f32, String, f32, f32, f32, f32)> = None;
        for wall in walls {
            let id = match wall.get("id").and_then(Value::as_str) {
                Some(id) => id.to_string(),
                None => continue,
            };
            let points: Vec<(f32, f32)> = wall
                .get("points")
                .and_then(Value::as_array)
                .map(|items| {
                    items
                        .iter()
                        .filter_map(|point| {
                            Some((
                                point.get(0)?.as_f64()? as f32,
                                point.get(1)?.as_f64()? as f32,
                            ))
                        })
                        .collect()
                })
                .unwrap_or_default();
            for pair in points.windows(2) {
                let (ax, ay) = pair[0];
                let (bx, by) = pair[1];
                let dx = bx - ax;
                let dy = by - ay;
                let length = (dx * dx + dy * dy).sqrt();
                if length < 1.0 {
                    continue;
                }
                let t = (((x - ax) * dx + (y - ay) * dy) / (length * length)).clamp(0.0, 1.0);
                let px = ax + t * dx;
                let py = ay + t * dy;
                let distance = ((x - px).powi(2) + (y - py).powi(2)).sqrt();
                if best.as_ref().map(|item| distance < item.0).unwrap_or(true) {
                    best = Some((distance, id.clone(), ax, ay, bx, by));
                }
            }
        }
        let Some((distance, wall_id, ax, ay, bx, by)) = best else {
            self.notice = "рядом нет стены для двери".to_string();
            return;
        };
        if distance > 30.0 {
            self.notice = "дверь ставится на стену — кликните ближе".to_string();
            return;
        }
        let length = ((bx - ax).powi(2) + (by - ay).powi(2)).sqrt();
        let ux = (bx - ax) / length;
        let uy = (by - ay) / length;
        let t = (((x - ax) * (bx - ax) + (y - ay) * (by - ay)) / (length * length)).clamp(0.0, 1.0);
        let px = ax + t * (bx - ax);
        let py = ay + t * (by - ay);
        let half = 22.0;
        let from = [
            (px - ux * half).round() as i64,
            (py - uy * half).round() as i64,
        ];
        let to = [
            (px + ux * half).round() as i64,
            (py + uy * half).round() as i64,
        ];
        let id = format!(
            "d{}",
            100000
                + std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .map(|value| value.as_millis() as u64 % 900000)
                    .unwrap_or(0)
        );
        if let Some(doors) = scene.doors.as_array_mut() {
            doors.push(serde_json::json!({
                "id": id,
                "wall_id": wall_id,
                "type": "normal",
                "from": from,
                "to": to
            }));
        }
        self.notice = "дверь добавлена — не забудьте сохранить".to_string();
    }

    fn save_scene_annotation(&mut self) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        let scene = match &self.scene {
            Some(scene) => scene.clone(),
            None => return,
        };
        let annotation = serde_json::json!({
            "format_version": 1,
            "image": {"width": scene.image.width, "height": scene.image.height},
            "grid": scene.grid,
            "walls": scene.walls,
            "doors": scene.doors,
            "areas": scene.areas,
        });
        match self.put_json::<Scene>(
            &format!(
                "/v1/campaigns/{}/scenes/{}/annotation?version={}",
                campaign.id, scene.id, scene.version
            ),
            annotation,
        ) {
            Ok(updated) => {
                self.scene = Some(updated);
                self.edit_mode = EditMode::Off;
                self.edit_points.clear();
                self.notice = "разметка сцены сохранена".to_string();
            }
            Err(error) => self.error = error,
        }
    }

    fn ws_url(&self, campaign_id: &str, scene_id: &str) -> String {
        let base = if let Some(rest) = self.base_url.strip_prefix("https://") {
            format!("wss://{rest}")
        } else if let Some(rest) = self.base_url.strip_prefix("http://") {
            format!("ws://{rest}")
        } else {
            self.base_url.clone()
        };
        format!(
            "{}/v1/campaigns/{campaign_id}/table?scene_id={scene_id}",
            base.trim_end_matches('/')
        )
    }

    fn connect_table(&mut self, campaign_id: &str, scene_id: &str) {
        self.chat_rx = None;
        self.chat_lines.clear();
        self.tokens.clear();
        self.doors.clear();
        self.turn_token = None;
        self.round = 0;
        self.visibility = None;
        self.pending_visibility = None;
        self.fog_texture = None;
        self.preview_account.clear();
        self.preview_name = None;
        self.drag_token = None;
        self.chat_status = "подключение…".to_string();
        let (token, url) = match (&self.token, self.ws_url(campaign_id, scene_id)) {
            (Some(token), url) => (token.clone(), url),
            _ => return,
        };
        let (sender, receiver) = mpsc::channel();
        thread::spawn(move || {
            let mut request = match url.as_str().into_client_request() {
                Ok(request) => request,
                Err(error) => {
                    let _ = sender.send(ChatEvent::Error(error.to_string()));
                    let _ = sender.send(ChatEvent::Disconnected(error.to_string()));
                    return;
                }
            };
            if let Ok(value) = HeaderValue::from_str(&format!("Bearer {token}")) {
                request.headers_mut().insert("Authorization", value);
            }
            let (mut socket, _) = match tungstenite::connect(request) {
                Ok(pair) => pair,
                Err(error) => {
                    let _ = sender.send(ChatEvent::Error(error.to_string()));
                    let _ = sender.send(ChatEvent::Disconnected(error.to_string()));
                    return;
                }
            };
            let _ = sender.send(ChatEvent::Connected);
            loop {
                match socket.read() {
                    Ok(tungstenite::Message::Text(text)) => {
                        let text: &str = &text;
                        let value: Value = match serde_json::from_str(text) {
                            Ok(value) => value,
                            Err(_) => continue,
                        };
                        match value["type"].as_str() {
                            Some("hello") => {
                                if let Some(items) = value["state"]["tokens"].as_array() {
                                    let tokens: Vec<Token> = items
                                        .iter()
                                        .filter_map(|item| {
                                            serde_json::from_value(item.clone()).ok()
                                        })
                                        .collect();
                                    let _ = sender.send(ChatEvent::Tokens(tokens));
                                }
                                if let Some(object) = value["state"]["doors"].as_object() {
                                    let doors: HashMap<String, bool> = object
                                        .iter()
                                        .filter_map(|(id, open)| {
                                            Some((id.clone(), open.as_bool()?))
                                        })
                                        .collect();
                                    let _ = sender.send(ChatEvent::Doors(doors));
                                }
                                if let Some(turn) = value["state"]["turn"].as_object() {
                                    let token_id = turn["token_id"]
                                        .as_str()
                                        .filter(|id| !id.is_empty())
                                        .map(str::to_string);
                                    let round = turn["round"].as_u64().unwrap_or(0) as u32;
                                    let _ = sender.send(ChatEvent::Turn(token_id, round));
                                }
                                if let Ok(mask) = serde_json::from_value::<VisibilityMask>(
                                    value["state"]["visibility"].clone(),
                                ) {
                                    let _ = sender.send(ChatEvent::Visibility(mask, None));
                                }
                                if let Some(items) = value["state"]["chat"].as_array() {
                                    let lines = items
                                        .iter()
                                        .filter_map(|item| {
                                            let login = item["actor"]["login"].as_str()?;
                                            let text = item["text"].as_str()?;
                                            let channel =
                                                item["channel"].as_str().unwrap_or("table");
                                            let line = match channel {
                                                "master" => {
                                                    format!("{login} → мастеру: {text}")
                                                }
                                                "whisper" => {
                                                    let to =
                                                        item["to_login"].as_str().unwrap_or("?");
                                                    format!("{login} → {to}: {text}")
                                                }
                                                _ => format!("{login}: {text}"),
                                            };
                                            Some(line)
                                        })
                                        .collect();
                                    let _ = sender.send(ChatEvent::History(lines));
                                }
                            }
                            Some("chat.said") => {
                                let actor = value["actor"]["login"].as_str().unwrap_or("?");
                                let body = value["text"].as_str().unwrap_or("");
                                let channel = value["channel"].as_str().unwrap_or("table");
                                let line = match channel {
                                    "master" => format!("{actor} → мастеру: {body}"),
                                    "whisper" => {
                                        let to = value["to"]["login"].as_str().unwrap_or("?");
                                        format!("{actor} → {to}: {body}")
                                    }
                                    _ => format!("{actor}: {body}"),
                                };
                                let _ = sender.send(ChatEvent::Message(line));
                            }
                            Some("scene.shown") => {
                                if let Ok(scene) =
                                    serde_json::from_value::<SceneSummary>(value["scene"].clone())
                                {
                                    let tokens: Vec<Token> = value["tokens"]
                                        .as_array()
                                        .map(|items| {
                                            items
                                                .iter()
                                                .filter_map(|item| {
                                                    serde_json::from_value(item.clone()).ok()
                                                })
                                                .collect()
                                        })
                                        .unwrap_or_default();
                                    let doors: HashMap<String, bool> = value["doors"]
                                        .as_object()
                                        .map(|object| {
                                            object
                                                .iter()
                                                .filter_map(|(id, open)| {
                                                    Some((id.clone(), open.as_bool()?))
                                                })
                                                .collect()
                                        })
                                        .unwrap_or_default();
                                    let turn_id = value["turn"]["token_id"]
                                        .as_str()
                                        .filter(|id| !id.is_empty())
                                        .map(str::to_string);
                                    let turn_round =
                                        value["turn"]["round"].as_u64().unwrap_or(0) as u32;
                                    let _ = sender.send(ChatEvent::SceneShown(
                                        scene, tokens, doors, turn_id, turn_round,
                                    ));
                                }
                            }
                            Some("scene.changed") => {
                                if let Some(scene_id) = value["scene_id"].as_str() {
                                    let _ =
                                        sender.send(ChatEvent::SceneChanged(scene_id.to_string()));
                                }
                            }
                            Some("token.placed") => {
                                if let Ok(token) =
                                    serde_json::from_value::<Token>(value["token"].clone())
                                {
                                    let _ = sender.send(ChatEvent::TokenPlaced(token));
                                }
                            }
                            Some("token.moved") => {
                                if let Ok(token) =
                                    serde_json::from_value::<Token>(value["token"].clone())
                                {
                                    let _ = sender.send(ChatEvent::TokenMoved(token));
                                }
                            }
                            Some("token.updated") => {
                                if let Ok(token) =
                                    serde_json::from_value::<Token>(value["token"].clone())
                                {
                                    let _ = sender.send(ChatEvent::TokenUpdated(token));
                                }
                            }
                            Some("token.removed") => {
                                if let Some(token_id) = value["token_id"].as_str() {
                                    let _ =
                                        sender.send(ChatEvent::TokenRemoved(token_id.to_string()));
                                }
                            }
                            Some("initiative.rolled") => {
                                if let Ok(token) =
                                    serde_json::from_value::<Token>(value["token"].clone())
                                {
                                    let formula =
                                        value["formula"].as_str().unwrap_or("1d20").to_string();
                                    let total = value["total"].as_i64().unwrap_or(0);
                                    let _ = sender
                                        .send(ChatEvent::InitiativeRolled(token, formula, total));
                                }
                            }
                            Some("combat.reset") => {
                                let tokens: Vec<Token> = value["tokens"]
                                    .as_array()
                                    .map(|items| {
                                        items
                                            .iter()
                                            .filter_map(|item| {
                                                serde_json::from_value(item.clone()).ok()
                                            })
                                            .collect()
                                    })
                                    .unwrap_or_default();
                                let cleared =
                                    value["initiative_cleared"].as_bool().unwrap_or(false);
                                let _ = sender.send(ChatEvent::CombatReset(tokens, cleared));
                            }
                            Some("character.changed") => {
                                if let Ok(character) =
                                    serde_json::from_value::<Character>(value["character"].clone())
                                {
                                    let _ = sender.send(ChatEvent::CharacterChanged(character));
                                }
                            }
                            Some("note.changed") => {
                                if let Ok(note) =
                                    serde_json::from_value::<Note>(value["note"].clone())
                                {
                                    let _ = sender.send(ChatEvent::NoteChanged(note));
                                }
                            }
                            Some("note.removed") => {
                                if let Some(note_id) = value["note_id"].as_str() {
                                    let _ =
                                        sender.send(ChatEvent::NoteRemoved(note_id.to_string()));
                                }
                            }
                            Some("members.changed") => {
                                let _ = sender.send(ChatEvent::MembersChanged);
                            }
                            Some("roll") => {
                                let actor = value["actor"]["login"].as_str().unwrap_or("?");
                                let formula = value["formula"].as_str().unwrap_or("?");
                                let total = value["total"].as_i64().unwrap_or(0);
                                let values = value["values"]
                                    .as_array()
                                    .map(|items| {
                                        items
                                            .iter()
                                            .filter_map(|item| item.as_i64())
                                            .map(|item| item.to_string())
                                            .collect::<Vec<_>>()
                                            .join(", ")
                                    })
                                    .unwrap_or_default();
                                let _ = sender.send(ChatEvent::Roll(format!(
                                    "бросок {actor}: {formula} = {total}  [{values}]"
                                )));
                            }
                            Some("turn.set") => {
                                let token_id = value["token_id"]
                                    .as_str()
                                    .filter(|id| !id.is_empty())
                                    .map(str::to_string);
                                let round = value["round"].as_u64().unwrap_or(0) as u32;
                                let _ = sender.send(ChatEvent::Turn(token_id, round));
                            }
                            Some("door.set") => {
                                if let (Some(door_id), Some(open)) =
                                    (value["door_id"].as_str(), value["open"].as_bool())
                                {
                                    let _ =
                                        sender.send(ChatEvent::DoorSet(door_id.to_string(), open));
                                }
                            }
                            Some("visibility") => {
                                if let Ok(mask) =
                                    serde_json::from_value::<VisibilityMask>(value["mask"].clone())
                                {
                                    let as_login =
                                        value["as_account"]["login"].as_str().map(str::to_string);
                                    let _ = sender.send(ChatEvent::Visibility(mask, as_login));
                                }
                            }
                            Some("error") => {
                                let message =
                                    value["error"]["message"].as_str().unwrap_or("ошибка");
                                let _ = sender.send(ChatEvent::Error(message.to_string()));
                            }
                            _ => {}
                        }
                    }
                    Ok(tungstenite::Message::Close(_)) => break,
                    Ok(_) => {}
                    Err(error) => {
                        let _ = sender.send(ChatEvent::Error(error.to_string()));
                        break;
                    }
                }
            }
            let _ = sender.send(ChatEvent::Disconnected("соединение закрыто".to_string()));
        });
        self.chat_rx = Some(receiver);
    }

    fn poll_chat(&mut self) {
        let mut events = Vec::new();
        if let Some(receiver) = &self.chat_rx {
            while let Ok(event) = receiver.try_recv() {
                events.push(event);
            }
        }
        for event in events {
            match event {
                ChatEvent::Connected => {
                    self.chat_status = "на связи".to_string();
                    self.reconnect_at = None;
                    self.reconnect_attempts = 0;
                }
                ChatEvent::History(lines) => self.chat_lines = lines,
                ChatEvent::Message(line) => self.chat_lines.push(line),
                ChatEvent::Tokens(list) => {
                    let scene_id = self.current_scene_id();
                    self.tokens = list
                        .into_iter()
                        .filter(|token| token.scene_id == scene_id)
                        .collect();
                }
                ChatEvent::TokenPlaced(token) => {
                    if token.scene_id == self.current_scene_id()
                        && !self.tokens.iter().any(|item| item.id == token.id)
                    {
                        self.tokens.push(token);
                    }
                }
                ChatEvent::TokenMoved(token) => {
                    if let Some(item) = self.tokens.iter_mut().find(|item| item.id == token.id) {
                        *item = token;
                    }
                }
                ChatEvent::TokenUpdated(token) => {
                    if let Some(item) = self.tokens.iter_mut().find(|item| item.id == token.id) {
                        *item = token;
                    }
                }
                ChatEvent::TokenRemoved(token_id) => {
                    self.tokens.retain(|token| token.id != token_id);
                    if self.turn_token.as_deref() == Some(token_id.as_str()) {
                        self.turn_token = None;
                        self.round = 0;
                    }
                }
                ChatEvent::InitiativeRolled(token, formula, total) => {
                    let name = token.name.clone();
                    if let Some(item) = self.tokens.iter_mut().find(|item| item.id == token.id) {
                        *item = token;
                    }
                    self.chat_lines
                        .push(format!("инициатива {name}: {total} ({formula})"));
                }
                ChatEvent::CombatReset(tokens, cleared) => {
                    let scene_id = self.current_scene_id();
                    self.tokens = tokens
                        .into_iter()
                        .filter(|token| token.scene_id == scene_id)
                        .collect();
                    self.turn_token = None;
                    self.round = 0;
                    self.chat_lines.push(if cleared {
                        "бой сброшен, инициатива обнулена".to_string()
                    } else {
                        "бой сброшен".to_string()
                    });
                }
                ChatEvent::CharacterChanged(character) => {
                    let (hp, hp_max) = character_hp(&character);
                    if let Some(item) = self
                        .characters
                        .iter_mut()
                        .find(|item| item.id == character.id)
                    {
                        *item = character.clone();
                    } else {
                        self.characters.push(character.clone());
                    }
                    for token in &mut self.tokens {
                        if token.character_id == character.id {
                            token.hp = hp;
                            token.hp_max = hp_max;
                            token.character_name = character.name.clone();
                        }
                    }
                    if self.selected_character.as_deref() == Some(character.id.as_str()) {
                        self.sheet_hp = hp;
                        self.sheet_hp_max = hp_max;
                    }
                }
                ChatEvent::NoteChanged(note) => {
                    if let Some(item) = self.notes.iter_mut().find(|item| item.id == note.id) {
                        *item = note;
                    } else {
                        self.notes.push(note);
                    }
                    self.notes.sort_by(|a, b| {
                        b.updated_at
                            .cmp(&a.updated_at)
                            .then_with(|| b.id.cmp(&a.id))
                    });
                }
                ChatEvent::NoteRemoved(note_id) => {
                    self.notes.retain(|note| note.id != note_id);
                    if self.selected_note.as_deref() == Some(note_id.as_str()) {
                        self.selected_note = None;
                        self.note_title.clear();
                        self.note_body.clear();
                    }
                    self.load_notes();
                }
                ChatEvent::MembersChanged => self.refresh_campaign_detail(),
                ChatEvent::Roll(line) => self.chat_lines.push(line),
                ChatEvent::Turn(token_id, round) => {
                    self.turn_token = token_id;
                    self.round = round;
                }
                ChatEvent::SceneShown(scene, tokens, doors, turn_id, turn_round) => {
                    if self.load_scene(&scene, Some(tokens)) {
                        self.doors = doors;
                        self.turn_token = turn_id;
                        self.round = turn_round;
                        for item in &mut self.scenes {
                            item.active = item.id == scene.id;
                        }
                    }
                }
                ChatEvent::SceneChanged(id) => {
                    if id == self.current_scene_id() {
                        if let Some(summary) =
                            self.scenes.iter().find(|scene| scene.id == id).cloned()
                        {
                            self.edit_mode = EditMode::Off;
                            self.edit_points.clear();
                            self.load_scene(&summary, None);
                        }
                    }
                }
                ChatEvent::Doors(doors) => self.doors = doors,
                ChatEvent::Visibility(mask, as_login) => {
                    if let Some(login) = as_login {
                        self.preview_name = Some(login);
                    }
                    self.visibility = Some(mask.clone());
                    self.pending_visibility = Some(mask);
                }
                ChatEvent::DoorSet(id, open) => {
                    self.doors.insert(id, open);
                }
                ChatEvent::Disconnected(reason) => {
                    self.chat_rx = None;
                    self.reconnect_attempts = self.reconnect_attempts.saturating_add(1);
                    if self.reconnect_attempts <= 5 {
                        self.chat_status = "нет связи — переподключаюсь…".to_string();
                        self.reconnect_at =
                            Some(std::time::Instant::now() + std::time::Duration::from_secs(2));
                    } else {
                        self.chat_status = format!("нет связи: {reason}");
                        self.reconnect_at = None;
                    }
                }
                ChatEvent::Error(error) => self.chat_status = format!("ошибка: {error}"),
            }
        }
    }

    fn tick_reconnect(&mut self) {
        if self.screen != Screen::Scene || self.chat_rx.is_some() {
            return;
        }
        let Some(at) = self.reconnect_at else {
            return;
        };
        if std::time::Instant::now() < at {
            return;
        }
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => {
                self.reconnect_at = None;
                return;
            }
        };
        let scene_id = self.current_scene_id();
        if scene_id.is_empty() {
            self.reconnect_at = None;
            return;
        }
        self.reconnect_at = None;
        self.chat_status = "переподключение…".to_string();
        self.connect_table(&campaign.id, &scene_id);
    }

    fn current_scene_id(&self) -> String {
        self.scene
            .as_ref()
            .map(|scene| scene.id.clone())
            .unwrap_or_default()
    }

    fn is_master(&self) -> bool {
        self.campaign
            .as_ref()
            .map(|campaign| campaign.role == "owner" || campaign.role == "master")
            .unwrap_or(false)
    }

    fn can_move_token(&self, token: &Token) -> bool {
        if self.is_master() {
            return true;
        }
        self.account
            .as_ref()
            .map(|account| !token.owner_id.is_empty() && token.owner_id == account.id)
            .unwrap_or(false)
    }

    fn send_intent(&self, payload: Value) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        let token = match &self.token {
            Some(token) => token.clone(),
            None => return,
        };
        let url = self.ws_url(&campaign.id, &self.current_scene_id());
        thread::spawn(move || {
            let mut request = match url.as_str().into_client_request() {
                Ok(request) => request,
                Err(_) => return,
            };
            if let Ok(value) = HeaderValue::from_str(&format!("Bearer {token}")) {
                request.headers_mut().insert("Authorization", value);
            }
            let (mut socket, _) = match tungstenite::connect(request) {
                Ok(pair) => pair,
                Err(error) => {
                    eprintln!("стол: {error}");
                    return;
                }
            };
            let _ = socket.read();
            let _ = socket.send(tungstenite::Message::text(payload.to_string()));
            let _ = socket.read();
            let _ = socket.close(None);
        });
    }

    fn chat_send(&mut self) {
        let text = self.chat_input.trim().to_string();
        if text.is_empty() {
            return;
        }
        if self.chat_channel == "whisper" && self.chat_to.is_empty() {
            self.notice = "шёпот: выберите получателя".to_string();
            return;
        }
        self.chat_input.clear();
        let mut payload = serde_json::json!({
            "type": "chat.say",
            "text": text,
            "channel": self.chat_channel,
        });
        if self.chat_channel == "whisper" {
            payload["to"] = Value::String(self.chat_to.clone());
        }
        self.send_intent(payload);
    }

    fn place_token(&mut self) {
        let scene = match &self.scene {
            Some(scene) => scene.clone(),
            None => return,
        };
        let name = self.token_name.trim().to_string();
        let (x, y) = (
            scene.image.width as f64 / 2.0,
            scene.image.height as f64 / 2.0,
        );
        let mut payload = serde_json::json!({
            "type": "token.place",
            "name": if name.is_empty() { "Фишка" } else { name.as_str() },
            "x": x,
            "y": y,
            "darkvision_ft": self.token_darkvision,
        });
        if !self.token_owner.is_empty() {
            payload["owner_id"] = Value::String(self.token_owner.clone());
        }
        if !self.token_character.is_empty() {
            payload["character_id"] = Value::String(self.token_character.clone());
        }
        self.send_intent(payload);
    }

    fn roll_initiative(&mut self, token_id: &str) {
        self.send_intent(serde_json::json!({
            "type": "initiative.roll",
            "token_id": token_id,
        }));
    }

    fn roll_all_initiative(&mut self, start: bool) {
        let mut payload = serde_json::json!({"type": "initiative.roll_all"});
        if start {
            payload["start"] = Value::Bool(true);
        }
        self.send_intent(payload);
    }

    fn reset_combat(&mut self, clear_initiative: bool) {
        self.send_intent(serde_json::json!({
            "type": "combat.reset",
            "clear_initiative": clear_initiative,
        }));
    }

    fn remove_token(&mut self, token_id: &str) {
        self.confirm_token_remove = None;
        self.send_intent(serde_json::json!({
            "type": "token.remove",
            "token_id": token_id,
        }));
    }

    fn link_token(&mut self, token_id: &str, character_id: &str) {
        self.send_intent(serde_json::json!({
            "type": "token.update",
            "token_id": token_id,
            "character_id": character_id,
        }));
    }

    fn update_token(
        &mut self,
        token_id: &str,
        hp: i32,
        hp_max: i32,
        initiative: i32,
        darkvision: i32,
    ) {
        self.send_intent(serde_json::json!({
            "type": "token.update",
            "token_id": token_id,
            "hp": hp,
            "hp_max": hp_max,
            "initiative": initiative,
            "darkvision_ft": darkvision,
        }));
    }

    fn roll_dice(&mut self) {
        let formula = self.roll_formula.trim().to_string();
        if formula.is_empty() {
            return;
        }
        self.send_intent(serde_json::json!({
            "type": "roll",
            "formula": formula,
            "visibility": "table",
        }));
    }

    fn roll_ability(&mut self, label: &str, score: i32) {
        let formula = ability_formula(score);
        self.roll_formula = formula.clone();
        self.send_intent(serde_json::json!({
            "type": "roll",
            "formula": formula,
            "visibility": "table",
        }));
        self.notice = format!("бросок {label}: {formula}");
    }

    fn next_turn(&mut self) {
        self.send_intent(serde_json::json!({"type": "turn.next"}));
    }

    fn request_preview(&mut self, account_id: &str, login: &str) {
        self.preview_account = account_id.to_string();
        self.preview_name = Some(login.to_string());
        self.visibility = None;
        self.fog_texture = None;
        self.send_intent(serde_json::json!({
            "type": "visibility.peek",
            "account_id": account_id,
        }));
    }

    fn clear_preview(&mut self) {
        self.preview_account.clear();
        self.preview_name = None;
        self.visibility = None;
        self.pending_visibility = None;
        self.fog_texture = None;
        self.send_intent(serde_json::json!({
            "type": "visibility.peek",
            "account_id": "",
        }));
    }

    fn move_token(&self, token_id: &str, x: f64, y: f64) {
        self.send_intent(serde_json::json!({
            "type": "token.move",
            "token_id": token_id,
            "x": x,
            "y": y,
        }));
    }

    fn show_chat(&mut self, ui: &mut egui::Ui) {
        ui.label(
            egui::RichText::new("Стол")
                .size(24.0)
                .strong()
                .color(WARM_ACCENT),
        );
        ui.label(
            egui::RichText::new(&self.chat_status)
                .size(15.0)
                .color(WARM_MUTED),
        );
        if !self.is_master() && self.panel_tab == PanelTab::Master {
            self.panel_tab = PanelTab::Chat;
        }
        ui.horizontal(|ui| {
            if ui
                .selectable_label(self.panel_tab == PanelTab::Chat, "Чат")
                .clicked()
            {
                self.panel_tab = PanelTab::Chat;
            }
            if ui
                .selectable_label(self.panel_tab == PanelTab::Combat, "Бой")
                .clicked()
            {
                self.panel_tab = PanelTab::Combat;
            }
            if ui
                .selectable_label(self.panel_tab == PanelTab::Sheet, "Лист")
                .clicked()
            {
                self.panel_tab = PanelTab::Sheet;
            }
            if ui
                .selectable_label(self.panel_tab == PanelTab::Journal, "Журнал")
                .clicked()
            {
                self.panel_tab = PanelTab::Journal;
            }
            if self.is_master()
                && ui
                    .selectable_label(self.panel_tab == PanelTab::Master, "Мастер")
                    .clicked()
            {
                self.panel_tab = PanelTab::Master;
            }
        });
        ui.separator();
        if self.panel_tab == PanelTab::Chat {
            egui::ScrollArea::vertical()
                .stick_to_bottom(true)
                .auto_shrink([false, false])
                .max_height(220.0)
                .show(ui, |ui| {
                    if self.chat_lines.is_empty() {
                        ui.label(
                            egui::RichText::new("сообщений пока нет")
                                .size(15.0)
                                .color(WARM_MUTED),
                        );
                    }
                    for line in &self.chat_lines {
                        ui.label(line);
                    }
                });
            ui.separator();
            ui.horizontal(|ui| {
                if ui
                    .selectable_label(self.chat_channel == "table", "Стол")
                    .clicked()
                {
                    self.chat_channel = "table".to_string();
                }
                if ui
                    .selectable_label(self.chat_channel == "master", "Мастеру")
                    .clicked()
                {
                    self.chat_channel = "master".to_string();
                }
                if ui
                    .selectable_label(self.chat_channel == "whisper", "Шёпот")
                    .clicked()
                {
                    self.chat_channel = "whisper".to_string();
                }
            });
            if self.chat_channel == "whisper" {
                let members = self
                    .campaign_detail
                    .as_ref()
                    .map(|detail| detail.members.clone())
                    .unwrap_or_default();
                let selected = if self.chat_to_login.is_empty() {
                    "кому".to_string()
                } else {
                    self.chat_to_login.clone()
                };
                egui::ComboBox::from_label("получатель")
                    .selected_text(selected)
                    .show_ui(ui, |ui| {
                        for member in &members {
                            let mine = self
                                .account
                                .as_ref()
                                .map(|account| account.id == member.id)
                                .unwrap_or(false);
                            if !mine {
                                ui.selectable_value(
                                    &mut self.chat_to,
                                    member.id.clone(),
                                    member.login.clone(),
                                );
                            }
                        }
                    });
                if let Some(member) = members.iter().find(|member| member.id == self.chat_to) {
                    self.chat_to_login = member.login.clone();
                }
            }
            ui.horizontal(|ui| {
                let response = ui.add(
                    egui::TextEdit::singleline(&mut self.chat_input)
                        .hint_text("сообщение столу")
                        .desired_width(220.0),
                );
                let entered =
                    response.lost_focus() && ui.input(|state| state.key_pressed(egui::Key::Enter));
                if ui.button("Сказать").clicked() || entered {
                    self.chat_send();
                }
            });
            ui.separator();
            ui.heading("Кубы");
            ui.horizontal(|ui| {
                ui.add(egui::TextEdit::singleline(&mut self.roll_formula).desired_width(90.0));
                if ui.button("Бросить").clicked() {
                    self.roll_dice();
                }
                if ui.button("d20").clicked() {
                    self.roll_formula = "1d20".to_string();
                    self.roll_dice();
                }
            });
        }
        if self.panel_tab == PanelTab::Master {
            if let Some(campaign) = &self.campaign {
                if campaign.role == "owner" || campaign.role == "master" {
                    let members = self
                        .campaign_detail
                        .as_ref()
                        .map(|detail| detail.members.clone())
                        .unwrap_or_default();
                    ui.separator();
                    ui.heading("Глазами игрока");
                    let preview_selected = members
                        .iter()
                        .find(|member| member.id == self.preview_account)
                        .map(|member| member.login.clone())
                        .unwrap_or_else(|| "выберите игрока".to_string());
                    egui::ComboBox::from_label("участник")
                        .selected_text(preview_selected)
                        .show_ui(ui, |ui| {
                            for member in &members {
                                if member.role == "player" || member.role == "observer" {
                                    ui.selectable_value(
                                        &mut self.preview_account,
                                        member.id.clone(),
                                        format!("{} ({})", member.login, member.role),
                                    );
                                }
                            }
                        });
                    if self.preview_name.is_some() {
                        if accent_button(ui, "Вернуться к мастеру", 240.0).clicked()
                        {
                            self.clear_preview();
                        }
                        if let Some(name) = &self.preview_name {
                            ui.label(
                                egui::RichText::new(format!("смотрим глазами {name}"))
                                    .strong()
                                    .color(WARM_ACCENT_HOVER),
                            );
                        }
                    } else if accent_button(ui, "Смотреть глазами", 240.0).clicked()
                    {
                        if let Some(member) = members
                            .iter()
                            .find(|member| member.id == self.preview_account)
                        {
                            let (id, login) = (member.id.clone(), member.login.clone());
                            self.request_preview(&id, &login);
                        }
                    }
                    ui.separator();
                    ui.heading("Фишки");
                    ui.add(
                        egui::TextEdit::singleline(&mut self.token_name)
                            .hint_text("имя фишки")
                            .desired_width(200.0),
                    );
                    let selected = members
                        .iter()
                        .find(|member| member.id == self.token_owner)
                        .map(|member| member.login.clone())
                        .unwrap_or_else(|| "я (мастер)".to_string());
                    egui::ComboBox::from_label("владелец")
                        .selected_text(selected)
                        .show_ui(ui, |ui| {
                            ui.selectable_value(&mut self.token_owner, String::new(), "я (мастер)");
                            for member in &members {
                                ui.selectable_value(
                                    &mut self.token_owner,
                                    member.id.clone(),
                                    format!("{} ({})", member.login, member.role),
                                );
                            }
                        });
                    let characters = self.characters.clone();
                    let character_label = characters
                        .iter()
                        .find(|character| character.id == self.token_character)
                        .map(|character| character.name.clone())
                        .unwrap_or_else(|| "без листа".to_string());
                    let before_character = self.token_character.clone();
                    egui::ComboBox::from_label("лист")
                        .selected_text(character_label)
                        .show_ui(ui, |ui| {
                            ui.selectable_value(
                                &mut self.token_character,
                                String::new(),
                                "без листа",
                            );
                            for character in &characters {
                                ui.selectable_value(
                                    &mut self.token_character,
                                    character.id.clone(),
                                    format!("{} ({})", character.name, character.owner_login),
                                );
                            }
                        });
                    if before_character != self.token_character {
                        if let Some(character) = characters
                            .iter()
                            .find(|character| character.id == self.token_character)
                        {
                            self.token_name = character.name.clone();
                            if !character.owner_id.is_empty() {
                                self.token_owner = character.owner_id.clone();
                            }
                        }
                    }
                    ui.horizontal(|ui| {
                        ui.label("тёмное зрение, фт");
                        ui.add(egui::DragValue::new(&mut self.token_darkvision).range(0..=300));
                    });
                    if ui.button("+ в центр сцены").clicked() {
                        self.place_token();
                    }
                    ui.label("перетащите фишку по карте");

                    let door_ids: Vec<String> = self
                        .scene
                        .as_ref()
                        .and_then(|scene| scene.doors.as_array())
                        .map(|doors| {
                            doors
                                .iter()
                                .filter_map(|door| door.get("id")?.as_str().map(str::to_string))
                                .collect()
                        })
                        .unwrap_or_default();
                    if !door_ids.is_empty() {
                        ui.separator();
                        ui.heading("Двери");
                        let mut toggle = None;
                        for id in door_ids {
                            let mut open = self.doors.get(&id).copied().unwrap_or(false);
                            if ui.checkbox(&mut open, &id).changed() {
                                toggle = Some((id, open));
                            }
                        }
                        if let Some((id, open)) = toggle {
                            self.doors.insert(id.clone(), open);
                            self.send_intent(serde_json::json!({
                                "type": "door.set",
                                "door_id": id,
                                "open": open,
                            }));
                        }
                    }
                }
            }
        }
        if self.panel_tab == PanelTab::Sheet {
            ui.separator();
            ui.heading("Лист персонажа");
            let characters = self.characters.clone();
            if characters.is_empty() {
                ui.label(
                    egui::RichText::new("листов пока нет — создайте первый")
                        .size(15.0)
                        .color(WARM_MUTED),
                );
            }
            for character in &characters {
                let selected = self.selected_character.as_deref() == Some(character.id.as_str());
                ui.horizontal(|ui| {
                    if ui
                        .selectable_label(selected, format!("{}", character.name))
                        .clicked()
                    {
                        self.select_character(&character.id);
                    }
                    if !character.owner_login.is_empty() {
                        ui.weak(&character.owner_login);
                    } else {
                        ui.weak("без владельца");
                    }
                    if character.approved {
                        ui.weak("✓");
                    }
                });
            }
            ui.separator();
            let editable = self.selected_character.as_ref().is_some_and(|id| {
                self.is_master()
                    || self.characters.iter().any(|character| {
                        &character.id == id
                            && self
                                .account
                                .as_ref()
                                .is_some_and(|account| account.id == character.owner_id)
                    })
            });
            if self.selected_character.is_some() {
                ui.horizontal(|ui| {
                    ui.label("Имя");
                    ui.add(egui::TextEdit::singleline(&mut self.sheet_name).desired_width(170.0));
                });
                ui.horizontal(|ui| {
                    ui.label("Класс");
                    ui.add(egui::TextEdit::singleline(&mut self.sheet_class).desired_width(170.0));
                });
                ui.horizontal(|ui| {
                    ui.label("Уровень");
                    ui.add(egui::DragValue::new(&mut self.sheet_level).range(1..=30));
                    ui.label("HP");
                    ui.add(egui::DragValue::new(&mut self.sheet_hp).range(0..=9999));
                    ui.label("/");
                    ui.add(egui::DragValue::new(&mut self.sheet_hp_max).range(0..=9999));
                });
                egui::CollapsingHeader::new("Характеристики")
                    .default_open(true)
                    .show(ui, |ui| {
                        let mut roll = None;
                        ui.horizontal(|ui| {
                            if ability_cell(ui, "СИЛ", &mut self.sheet_str) {
                                roll = Some(("СИЛ", self.sheet_str));
                            }
                            if ability_cell(ui, "ЛОВ", &mut self.sheet_dex) {
                                roll = Some(("ЛОВ", self.sheet_dex));
                            }
                            if ability_cell(ui, "ТЕЛ", &mut self.sheet_con) {
                                roll = Some(("ТЕЛ", self.sheet_con));
                            }
                        });
                        ui.horizontal(|ui| {
                            if ability_cell(ui, "ИНТ", &mut self.sheet_int) {
                                roll = Some(("ИНТ", self.sheet_int));
                            }
                            if ability_cell(ui, "МДР", &mut self.sheet_wis) {
                                roll = Some(("МДР", self.sheet_wis));
                            }
                            if ability_cell(ui, "ХАР", &mut self.sheet_cha) {
                                roll = Some(("ХАР", self.sheet_cha));
                            }
                        });
                        if let Some((label, score)) = roll {
                            self.roll_ability(label, score);
                        }
                    });
                let level = self.sheet_level;
                let saves: Vec<(&str, &str, i32, bool)> = ABILITY_KEYS
                    .iter()
                    .map(|(key, label)| {
                        (
                            *key,
                            *label,
                            self.ability_score(key),
                            self.save_profs.contains(*key),
                        )
                    })
                    .collect();
                let mut save_roll = None;
                egui::CollapsingHeader::new("Спасброски").show(ui, |ui| {
                    for (key, label, score, proficient) in &saves {
                        let mut prof = *proficient;
                        ui.horizontal(|ui| {
                            if ui.checkbox(&mut prof, "").changed() {
                                if prof {
                                    self.save_profs.insert((*key).to_string());
                                } else {
                                    self.save_profs.remove(*key);
                                }
                            }
                            let mut bonus = ability_modifier(*score);
                            if prof {
                                bonus += proficiency_bonus(level);
                            }
                            if ui
                                .small_button(format!("{label}  d20 {bonus:+}"))
                                .on_hover_text(format!("спасбросок {label}"))
                                .clicked()
                            {
                                save_roll = Some((*label, *score, prof));
                            }
                        });
                    }
                });
                if let Some((label, score, proficient)) = save_roll {
                    self.roll_check(label, score, proficient);
                }
                let skills: Vec<(&str, &str, &str, i32, bool)> = SKILLS
                    .iter()
                    .map(|(key, label, ability)| {
                        (
                            *key,
                            *label,
                            *ability,
                            self.ability_score(ability),
                            self.skill_profs.contains(*key),
                        )
                    })
                    .collect();
                let mut skill_roll = None;
                egui::CollapsingHeader::new("Навыки").show(ui, |ui| {
                    for (key, label, _, score, proficient) in &skills {
                        let mut prof = *proficient;
                        ui.horizontal(|ui| {
                            if ui.checkbox(&mut prof, "").changed() {
                                if prof {
                                    self.skill_profs.insert((*key).to_string());
                                } else {
                                    self.skill_profs.remove(*key);
                                }
                            }
                            let mut bonus = ability_modifier(*score);
                            if prof {
                                bonus += proficiency_bonus(level);
                            }
                            if ui
                                .small_button(format!("{label}  d20 {bonus:+}"))
                                .on_hover_text(format!("проверка {label}"))
                                .clicked()
                            {
                                skill_roll = Some((*label, *score, prof));
                            }
                        });
                    }
                });
                if let Some((label, score, proficient)) = skill_roll {
                    self.roll_check(label, score, proficient);
                }
                egui::CollapsingHeader::new("Инвентарь").show(ui, |ui| {
                    ui.add(
                        egui::TextEdit::multiline(&mut self.sheet_inventory)
                            .hint_text("что несёт персонаж")
                            .desired_rows(3)
                            .desired_width(f32::INFINITY),
                    );
                });
                egui::CollapsingHeader::new("Заклинания").show(ui, |ui| {
                    ui.add(
                        egui::TextEdit::multiline(&mut self.sheet_spells)
                            .hint_text("известные и подготовленные")
                            .desired_rows(3)
                            .desired_width(f32::INFINITY),
                    );
                });
                ui.label("Заметки");
                ui.add(
                    egui::TextEdit::multiline(&mut self.sheet_notes)
                        .desired_rows(5)
                        .desired_width(f32::INFINITY),
                );
                if editable {
                    if accent_button(ui, "Сохранить лист", 220.0).clicked() {
                        self.save_character();
                    }
                } else {
                    ui.label(
                        egui::RichText::new("чужой лист — только просмотр")
                            .size(15.0)
                            .color(WARM_MUTED),
                    );
                }
            }
            ui.separator();
            ui.label("Новый лист");
            ui.horizontal(|ui| {
                ui.add(
                    egui::TextEdit::singleline(&mut self.new_character_name)
                        .hint_text("имя героя")
                        .desired_width(170.0),
                );
                if ui.button("Создать").clicked() {
                    self.create_character();
                }
            });
        }
        if self.panel_tab == PanelTab::Journal {
            ui.separator();
            ui.heading("Журнал кампании");
            let mut search = false;
            ui.horizontal(|ui| {
                let response = ui.add(
                    egui::TextEdit::singleline(&mut self.note_query)
                        .hint_text("поиск по заголовку и тексту")
                        .desired_width(200.0),
                );
                search |=
                    response.lost_focus() && ui.input(|state| state.key_pressed(egui::Key::Enter));
                if ui.button("Найти").clicked() {
                    search = true;
                }
                if !self.note_query.is_empty() && ui.button("Сброс").clicked() {
                    self.note_query.clear();
                    search = true;
                }
            });
            if search {
                self.load_notes();
            }
            let notes = self.notes.clone();
            if notes.is_empty() {
                ui.label(
                    egui::RichText::new("записей пока нет — начните журнал")
                        .size(15.0)
                        .color(WARM_MUTED),
                );
            }
            egui::ScrollArea::vertical()
                .auto_shrink([false, false])
                .max_height(160.0)
                .show(ui, |ui| {
                    for note in &notes {
                        let selected = self.selected_note.as_deref() == Some(note.id.as_str());
                        ui.horizontal(|ui| {
                            if selected {
                                ui.strong("▸");
                            }
                            let title = search_highlight(&note.title, &self.note_query, 15.0);
                            let response = ui.add(
                                egui::Label::new(title)
                                    .selectable(false)
                                    .sense(egui::Sense::click()),
                            );
                            if response.clicked() {
                                self.select_note(&note.id);
                            }
                            ui.weak(&note.author_login);
                            if note.visibility == "private" {
                                ui.weak("личная");
                            }
                        });
                        if !self.note_query.trim().is_empty() {
                            let snippet = search_snippet(&note.body, &self.note_query, 28, 90);
                            if !snippet.is_empty() {
                                ui.horizontal(|ui| {
                                    ui.add_space(18.0);
                                    ui.add(egui::Label::new(search_highlight(
                                        &snippet,
                                        &self.note_query,
                                        13.0,
                                    )));
                                });
                            }
                        }
                    }
                });
            ui.separator();
            if self.selected_note.is_some() {
                let editable = self.notes.iter().any(|note| {
                    self.selected_note.as_deref() == Some(note.id.as_str())
                        && self
                            .account
                            .as_ref()
                            .is_some_and(|account| account.id == note.author_id)
                });
                if editable {
                    ui.checkbox(&mut self.note_private, "только для меня");
                    ui.add(
                        egui::TextEdit::singleline(&mut self.note_title)
                            .hint_text("заголовок")
                            .desired_width(f32::INFINITY),
                    );
                    ui.add(
                        egui::TextEdit::multiline(&mut self.note_body)
                            .hint_text("текст заметки")
                            .desired_rows(8)
                            .desired_width(f32::INFINITY),
                    );
                    if accent_button(ui, "Сохранить запись", 240.0).clicked() {
                        self.save_note();
                    }
                    if ui.button("Удалить запись").clicked() {
                        self.delete_note();
                    }
                } else {
                    ui.label(
                        egui::RichText::new(&self.note_title)
                            .size(20.0)
                            .strong()
                            .color(WARM_ACCENT),
                    );
                    ui.label(
                        egui::RichText::new(&self.note_body)
                            .size(16.0)
                            .color(WARM_TEXT),
                    );
                }
            }
            ui.separator();
            ui.label("Новая запись");
            ui.horizontal(|ui| {
                ui.add(
                    egui::TextEdit::singleline(&mut self.new_note_title)
                        .hint_text("заголовок")
                        .desired_width(200.0),
                );
            });
            ui.horizontal(|ui| {
                ui.checkbox(&mut self.new_note_private, "только для меня");
                if ui.button("Создать").clicked() {
                    self.create_note();
                }
            });
        }
        if self.panel_tab == PanelTab::Combat {
            ui.separator();
            if self.round > 0 {
                ui.heading(format!("Бой · круг {}", self.round));
            } else {
                ui.heading("Бой");
            }
            if !self.is_master() {
                ui.label("перетащите свою фишку по карте");
            }
            let mut ordered = self.tokens.clone();
            ordered.sort_by(|a, b| {
                b.initiative
                    .cmp(&a.initiative)
                    .then_with(|| a.name.cmp(&b.name))
            });
            if ordered.is_empty() {
                ui.label("на сцене нет фишек");
            }
            let mut changed = None;
            for token in &ordered {
                let editable = self.can_move_token(token);
                let initiative_bonus = self
                    .characters
                    .iter()
                    .find(|character| character.id == token.character_id)
                    .map(|character| (sheet_ability(&character.data, "dex") - 10) / 2)
                    .unwrap_or(0);
                let current = self.turn_token.as_deref() == Some(token.id.as_str());
                let mine = self
                    .account
                    .as_ref()
                    .map(|account| account.id == token.owner_id)
                    .unwrap_or(false);
                ui.horizontal(|ui| {
                    if current {
                        ui.strong("▶");
                    }
                    if current && mine {
                        ui.strong("ваш ход");
                    }
                    ui.label(format!(
                        "{}  иниц. {}  HP {}/{}",
                        token.name, token.initiative, token.hp, token.hp_max
                    ));
                    if !token.owner_login.is_empty() {
                        ui.weak(&token.owner_login);
                    }
                    if !token.character_name.is_empty() {
                        ui.weak(format!("лист: {}", token.character_name));
                    }
                });
                if editable {
                    let mut hp = token.hp;
                    let mut hp_max = token.hp_max.max(1);
                    let mut initiative = token.initiative;
                    let mut darkvision = token.darkvision_ft;
                    let mut touched = false;
                    ui.horizontal(|ui| {
                        ui.label("HP");
                        touched |= ui
                            .add(egui::DragValue::new(&mut hp).range(0..=hp_max))
                            .changed();
                        ui.label("/");
                        touched |= ui
                            .add(egui::DragValue::new(&mut hp_max).range(1..=9999))
                            .changed();
                        ui.label("иниц.");
                        touched |= ui
                            .add(egui::DragValue::new(&mut initiative).range(-100..=1000))
                            .changed();
                    });
                    ui.horizontal(|ui| {
                        ui.label("тёмное зрение, фт");
                        touched |= ui
                            .add(egui::DragValue::new(&mut darkvision).range(0..=300))
                            .changed();
                    });
                    let initiative_label = if token.character_id.is_empty() {
                        "инициатива d20".to_string()
                    } else {
                        format!("инициатива d20 {initiative_bonus:+}")
                    };
                    if ui
                        .small_button(initiative_label)
                        .on_hover_text("бросить и записать инициативу")
                        .clicked()
                    {
                        self.roll_initiative(&token.id);
                    }
                    if touched {
                        changed = Some((token.id.clone(), hp, hp_max, initiative, darkvision));
                    }
                }
                if self.is_master() {
                    let characters = self.characters.clone();
                    let label = characters
                        .iter()
                        .find(|character| character.id == token.character_id)
                        .map(|character| character.name.clone())
                        .unwrap_or_else(|| "без листа".to_string());
                    let mut choice = token.character_id.clone();
                    ui.horizontal(|ui| {
                        egui::ComboBox::from_id_salt(format!("token-character-{}", token.id))
                            .selected_text(label)
                            .show_ui(ui, |ui| {
                                ui.selectable_value(&mut choice, String::new(), "без листа");
                                for character in &characters {
                                    ui.selectable_value(
                                        &mut choice,
                                        character.id.clone(),
                                        character.name.clone(),
                                    );
                                }
                            });
                    });
                    if choice != token.character_id {
                        self.link_token(&token.id, &choice);
                    }
                    if ui.small_button("удалить фишку").clicked() {
                        self.confirm_token_remove = Some(token.id.clone());
                    }
                }
            }
            if let Some(id) = self.confirm_token_remove.clone() {
                let name = ordered
                    .iter()
                    .find(|token| token.id == id)
                    .map(|token| token.name.clone())
                    .unwrap_or_else(|| "фишку".to_string());
                ui.horizontal(|ui| {
                    ui.label(
                        egui::RichText::new(format!("Убрать фишку «{name}»?")).color(WARM_DANGER),
                    );
                    if accent_button(ui, "Да", 80.0).clicked() {
                        self.remove_token(&id);
                    }
                    if ui.button("Отмена").clicked() {
                        self.confirm_token_remove = None;
                    }
                });
            }
            if let Some((id, hp, hp_max, initiative, darkvision)) = changed {
                self.update_token(&id, hp, hp_max, initiative, darkvision);
            }
            if self.is_master() {
                if ui
                    .button("Инициатива всем")
                    .on_hover_text("бросить d20 + ЛОВ для всех фишек сцены")
                    .clicked()
                {
                    self.roll_all_initiative(false);
                }
                if ui
                    .button("Инициатива и бой")
                    .on_hover_text("бросить всем и начать первый круг")
                    .clicked()
                {
                    self.roll_all_initiative(true);
                }
                egui::CollapsingHeader::new("Настройки боя").show(ui, |ui| {
                    if ui.button("Сбросить бой").clicked() {
                        self.reset_combat(false);
                    }
                    if ui
                        .button("Сбросить бой и инициативу")
                        .on_hover_text("обнулить инициативу всех фишек сцены")
                        .clicked()
                    {
                        self.reset_combat(true);
                    }
                });
                let label = if self.round == 0 {
                    "Начать бой"
                } else {
                    "Следующий ход"
                };
                if ui.button(label).clicked() {
                    self.next_turn();
                }
            }
        }
    }
}

impl eframe::App for App {
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        let ctx = ui.ctx().clone();
        ctx.request_repaint_after(std::time::Duration::from_millis(250));
        self.accept_dropped_files(&ctx);
        self.make_texture(&ctx);
        self.make_fog_texture(&ctx);
        self.poll_chat();
        self.tick_reconnect();
        let account_login = self.account.as_ref().map(|account| account.login.clone());
        let role_label = self
            .campaign
            .as_ref()
            .map(|campaign| role_name(&campaign.role));
        let connection = self.chat_status.clone();
        let preview_label = self.preview_name.clone();
        let logged = self.token.is_some();
        let mut logout = false;
        let mut open_settings = false;
        egui::Panel::top("top")
            .frame(warm_frame(WARM_PANEL).inner_margin(egui::Margin::symmetric(14, 8)))
            .show(ui, |ui| {
                ui.horizontal(|ui| {
                    ui.label(
                        egui::RichText::new("VTT")
                            .size(26.0)
                            .strong()
                            .color(WARM_ACCENT),
                    );
                    if let Some(login) = &account_login {
                        ui.separator();
                        match role_label {
                            Some(role) => ui.label(format!("{login} · {role}")),
                            None => ui.label(login),
                        };
                    }
                    if self.screen == Screen::Scene {
                        ui.separator();
                        ui.weak(&connection);
                    }
                    if let Some(name) = &preview_label {
                        ui.separator();
                        ui.label(
                            egui::RichText::new(format!("глазами {name}"))
                                .strong()
                                .color(WARM_ACCENT_HOVER),
                        );
                    }
                    if logged {
                        ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                            if ui.button("Настройки").clicked() {
                                open_settings = true;
                            }
                        });
                    }
                });
            });
        if open_settings {
            self.show_settings = true;
        }
        if self.show_settings {
            let mut open = true;
            let mut do_logout = false;
            egui::Window::new("Настройки")
                .open(&mut open)
                .collapsible(false)
                .resizable(false)
                .show(&ctx, |ui| {
                    if let Some(login) = &account_login {
                        ui.label(format!("аккаунт: {login}"));
                    }
                    if let Some(role) = role_label {
                        ui.label(format!("роль: {role}"));
                    }
                    ui.separator();
                    ui.label("сервер");
                    ui.monospace(&self.base_url);
                    ui.separator();
                    if ui.button("Выйти из аккаунта").clicked() {
                        do_logout = true;
                    }
                });
            self.show_settings = open;
            if do_logout {
                self.show_settings = false;
                logout = true;
            }
        }
        if logout {
            self.logout();
        }
        if !self.error.is_empty() && self.screen != Screen::Login {
            egui::Panel::bottom("error").show(ui, |ui| {
                ui.colored_label(egui::Color32::LIGHT_RED, &self.error);
            });
        }
        if !self.notice.is_empty() {
            egui::Panel::bottom("notice").show(ui, |ui| {
                ui.label(&self.notice);
            });
        }
        if self.screen == Screen::Scene {
            egui::Panel::right("chat")
                .resizable(true)
                .default_size(380.0)
                .frame(warm_frame(WARM_PANEL).inner_margin(egui::Margin::same(12)))
                .show(ui, |ui| {
                    egui::ScrollArea::vertical().show(ui, |ui| self.show_chat(ui));
                });
        }
        egui::CentralPanel::default().show(ui, |ui| match self.screen {
            Screen::Login => self.show_login(ui),
            Screen::Campaigns => self.show_campaigns(ui),
            Screen::Scenes => self.show_scenes(ui),
            Screen::Scene => self.show_scene(ui),
        });
    }
}

fn main() {
    let mut base_url = "http://127.0.0.1:8080".to_string();
    let mut install_dir: Option<PathBuf> = None;
    let mut args = std::env::args().skip(1);
    while let Some(arg) = args.next() {
        if arg == "--version" || arg == "-V" {
            println!("client {VERSION}");
            return;
        }
        if let Some(value) = arg.strip_prefix("--server=") {
            base_url = value.to_string();
        } else if arg == "--server" {
            if let Some(value) = args.next() {
                base_url = value;
            }
        } else if let Some(value) = arg.strip_prefix("--install-dir=") {
            install_dir = Some(PathBuf::from(value));
        } else if arg == "--install-dir" {
            install_dir = args.next().map(PathBuf::from);
        }
    }

    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default().with_inner_size([1440.0, 900.0]),
        ..Default::default()
    };
    if let Err(error) = eframe::run_native(
        "VTT",
        options,
        Box::new(move |cc| {
            Ok(Box::new(App::new(
                cc,
                base_url.clone(),
                install_dir.clone(),
            )))
        }),
    ) {
        eprintln!("client: {error}");
        std::process::exit(1);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn token_color_parses_hex() {
        let color = token_color("#e8c547");
        assert_eq!(color, egui::Color32::from_rgb(232, 197, 71));
        assert_eq!(token_color("nope"), egui::Color32::from_rgb(232, 197, 71));
    }

    #[test]
    fn multipart_has_both_files_and_name() {
        let (body, content_type) = multipart_body(b"image-bytes", b"{\"x\":1}", "Пещера");
        assert!(content_type.starts_with("multipart/form-data; boundary="));
        let text = String::from_utf8_lossy(&body);
        assert!(text.contains("name=\"image\""));
        assert!(text.contains("name=\"annotation\""));
        assert!(text.contains("name=\"name\""));
        assert!(text.contains("Пещера"));
        assert!(text.contains("image-bytes"));
        assert!(text.ends_with("--\r\n"));
    }

    #[test]
    fn token_without_stats_gets_defaults() {
        let token: Token = serde_json::from_str(
            r##"{"id":"t1","scene_id":"s1","name":"Старая","x":1,"y":2,"color":"#e8c547"}"##,
        )
        .expect("старая фишка");
        assert_eq!(token.hp, 10);
        assert_eq!(token.hp_max, 10);
        assert_eq!(token.initiative, 0);
        assert!(token.owner_login.is_empty());
    }

    #[test]
    fn search_highlight_finds_cyrillic() {
        let ranges = highlight_ranges("Дракон и дракон", "ДРАКОН");
        assert_eq!(ranges.len(), 2);
        assert_eq!(&"Дракон и дракон"[ranges[0].0..ranges[0].1], "Дракон");
        assert_eq!(&"Дракон и дракон"[ranges[1].0..ranges[1].1], "дракон");
        assert!(highlight_ranges("нет совпадений", "дракон").is_empty());
        let snippet = search_snippet(
            "В горах живёт старый дракон и охраняет клад.",
            "дракон",
            10,
            12,
        );
        assert!(snippet.contains("дракон"));
        assert!(snippet.starts_with('…'));
        assert!(snippet.ends_with('…'));
    }

    #[test]
    fn query_encode_percent_encodes_utf8() {
        assert_eq!(query_encode("dragon"), "dragon");
        assert_eq!(
            query_encode("таверна"),
            "%D1%82%D0%B0%D0%B2%D0%B5%D1%80%D0%BD%D0%B0"
        );
        assert_eq!(query_encode("a b+c"), "a%20b%2Bc");
    }

    #[test]
    fn ability_modifiers_match_d20() {
        assert_eq!(ability_modifier(1), -5);
        assert_eq!(ability_modifier(8), -1);
        assert_eq!(ability_modifier(10), 0);
        assert_eq!(ability_modifier(12), 1);
        assert_eq!(ability_modifier(16), 3);
        assert_eq!(ability_modifier(30), 10);
        assert_eq!(ability_formula(10), "1d20");
        assert_eq!(ability_formula(16), "1d20+3");
        assert_eq!(ability_formula(8), "1d20-1");
        assert_eq!(proficiency_bonus(1), 2);
        assert_eq!(proficiency_bonus(5), 3);
        assert_eq!(proficiency_bonus(9), 4);
        assert_eq!(proficiency_bonus(17), 6);
        assert_eq!(check_formula(0), "1d20");
        assert_eq!(check_formula(5), "1d20+5");
    }

    #[test]
    fn sheet_flags_read_proficiencies() {
        let data = serde_json::json!({
            "saves": {"dex": true, "wis": false},
            "skills": {"stealth": true}
        });
        assert!(sheet_flag(&data, "saves", "dex"));
        assert!(!sheet_flag(&data, "saves", "wis"));
        assert!(sheet_flag(&data, "skills", "stealth"));
        assert!(!sheet_flag(&data, "skills", "perception"));
    }

    #[test]
    fn sheet_ability_reads_data_and_defaults() {
        let data = serde_json::json!({
            "abilities": {"str": 16, "dex": 8, "con": 40},
            "hp": 4,
            "max_hp": 12
        });
        assert_eq!(sheet_ability(&data, "str"), 16);
        assert_eq!(sheet_ability(&data, "dex"), 8);
        assert_eq!(sheet_ability(&data, "con"), 30);
        assert_eq!(sheet_ability(&data, "wis"), 10);
        let character = Character {
            id: "h1".to_string(),
            owner_id: String::new(),
            owner_login: String::new(),
            name: "Мира".to_string(),
            approved: false,
            data,
        };
        assert_eq!(character_hp(&character), (4, 12));
    }

    #[test]
    fn demo_scene_has_png_and_annotation() {
        let (png, annotation) = demo_scene();
        let decoded = image::load_from_memory(&png).expect("демо-карта");
        assert_eq!(decoded.width(), 800);
        assert_eq!(decoded.height(), 600);
        let value: Value = serde_json::from_slice(&annotation).expect("демо-разметка");
        assert_eq!(value["format_version"], 1);
        assert_eq!(value["walls"].as_array().map(Vec::len), Some(5));
        assert_eq!(value["doors"].as_array().map(Vec::len), Some(1));
    }
}
