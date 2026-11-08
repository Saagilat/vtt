use std::collections::HashMap;
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
    Message(String),
    Tokens(Vec<Token>),
    TokenPlaced(Token),
    TokenMoved(Token),
    TokenUpdated(Token),
    SceneShown(SceneSummary, Vec<Token>, HashMap<String, bool>),
    SceneChanged(String),
    Doors(HashMap<String, bool>),
    DoorSet(String, bool),
    Visibility(VisibilityMask, Option<String>),
    Roll(String),
    Turn(Option<String>, u32),
    Error(String),
}

fn default_hp() -> i32 {
    10
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
    #[serde(default = "default_hp")]
    hp: i32,
    #[serde(default = "default_hp")]
    hp_max: i32,
    #[serde(default)]
    initiative: i32,
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
    #[allow(dead_code)]
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
enum PanelTab {
    Chat,
    Combat,
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
    chat_rx: Option<Receiver<ChatEvent>>,
    chat_input: String,
    chat_lines: Vec<String>,
    chat_status: String,
    tokens: Vec<Token>,
    token_name: String,
    token_owner: String,
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
            chat_lines: Vec::new(),
            chat_status: String::new(),
            tokens: Vec::new(),
            token_name: "Фишка".to_string(),
            token_owner: String::new(),
            roll_formula: "1d20".to_string(),
            preview_account: String::new(),
            preview_name: None,
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
        self.scenes.clear();
        self.scene = None;
        self.texture = None;
        self.chat_rx = None;
        self.chat_lines.clear();
        self.chat_status.clear();
        self.tokens.clear();
        self.drag_token = None;
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
        let me: Me = self.get_json("/v1/me")?;
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
        self.token_owner.clear();
        self.preview_account.clear();
        self.preview_name = None;
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
        if !self.load_scene(&summary, None) {
            return;
        }
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        self.screen = Screen::Scene;
        let scene_id = self.current_scene_id();
        self.connect_table(&campaign.id, &scene_id);
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
        ui.label(
            egui::RichText::new("Кампании")
                .size(30.0)
                .strong()
                .color(WARM_ACCENT),
        );
        ui.label(
            egui::RichText::new("выберите стол или создайте новый")
                .size(16.0)
                .color(WARM_MUTED),
        );
        ui.add_space(10.0);
        if self.campaigns.is_empty() {
            ui.label("пока пусто — создайте кампанию или войдите по коду");
        }
        let items = self.campaigns.clone();
        let card_width = ui.available_width().min(760.0);
        for campaign in items {
            let subtitle = format!("роль: {}", role_name(&campaign.role));
            if card_button(ui, &campaign.name, &subtitle, card_width).clicked() {
                self.open_campaign(campaign);
            }
            ui.add_space(6.0);
        }
        ui.add_space(12.0);
        warm_frame(WARM_PANEL).show(ui, |ui| {
            ui.set_width(card_width);
            ui.label(
                egui::RichText::new("Новая кампания")
                    .size(20.0)
                    .strong()
                    .color(WARM_TEXT),
            );
            ui.horizontal(|ui| {
                ui.add_sized(
                    [320.0, 40.0],
                    egui::TextEdit::singleline(&mut self.new_campaign).hint_text("название"),
                );
                if accent_button(ui, "Создать", 140.0).clicked() {
                    self.create_campaign();
                }
                if ui
                    .add_sized(
                        [150.0, 42.0],
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
        warm_frame(WARM_PANEL).show(ui, |ui| {
            ui.set_width(card_width);
            ui.label(
                egui::RichText::new("Войти по коду")
                    .size(20.0)
                    .strong()
                    .color(WARM_TEXT),
            );
            ui.label(
                egui::RichText::new("код приглашения выдаёт мастер кампании")
                    .size(15.0)
                    .color(WARM_MUTED),
            );
            ui.horizontal(|ui| {
                ui.add_sized(
                    [240.0, 40.0],
                    egui::TextEdit::singleline(&mut self.join_code).hint_text("код от мастера"),
                );
                if accent_button(ui, "Войти", 140.0).clicked() {
                    self.accept_invite();
                }
            });
        });
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
        if let Some(detail) = &self.campaign_detail {
            warm_frame(WARM_PANEL).show(ui, |ui| {
                ui.set_width(width);
                ui.label(
                    egui::RichText::new("Участники")
                        .size(18.0)
                        .strong()
                        .color(WARM_TEXT),
                );
                for member in &detail.members {
                    ui.label(format!("{}  ·  {}", member.login, role_name(&member.role)));
                }
            });
        }
        ui.add_space(10.0);
        ui.label(
            egui::RichText::new("Сцены")
                .size(22.0)
                .strong()
                .color(WARM_TEXT),
        );
        if self.scenes.is_empty() {
            ui.label("сцен нет — откройте «Импорт карты» ниже");
        }
        let items = self.scenes.clone();
        for scene in items {
            let subtitle = if scene.active {
                "● активна — открыть стол"
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
                            }
                            Some("chat.said") => {
                                let actor = value["actor"]["login"].as_str().unwrap_or("?");
                                let body = value["text"].as_str().unwrap_or("");
                                let _ = sender.send(ChatEvent::Message(format!("{actor}: {body}")));
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
                                    let _ =
                                        sender.send(ChatEvent::SceneShown(scene, tokens, doors));
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
                ChatEvent::Connected => self.chat_status = "на связи".to_string(),
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
                ChatEvent::Roll(line) => self.chat_lines.push(line),
                ChatEvent::Turn(token_id, round) => {
                    self.turn_token = token_id;
                    self.round = round;
                }
                ChatEvent::SceneShown(scene, tokens, doors) => {
                    if self.load_scene(&scene, Some(tokens)) {
                        self.doors = doors;
                        self.turn_token = None;
                        self.round = 0;
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
                ChatEvent::Error(error) => self.chat_status = format!("ошибка: {error}"),
            }
        }
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
        self.chat_input.clear();
        self.send_intent(serde_json::json!({"type": "chat.say", "text": text}));
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
        });
        if !self.token_owner.is_empty() {
            payload["owner_id"] = Value::String(self.token_owner.clone());
        }
        self.send_intent(payload);
    }

    fn update_token(&mut self, token_id: &str, hp: i32, hp_max: i32, initiative: i32) {
        self.send_intent(serde_json::json!({
            "type": "token.update",
            "token_id": token_id,
            "hp": hp,
            "hp_max": hp_max,
            "initiative": initiative,
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
                });
                if editable {
                    let mut hp = token.hp;
                    let mut hp_max = token.hp_max.max(1);
                    let mut initiative = token.initiative;
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
                    if touched {
                        changed = Some((token.id.clone(), hp, hp_max, initiative));
                    }
                }
            }
            if let Some((id, hp, hp_max, initiative)) = changed {
                self.update_token(&id, hp, hp_max, initiative);
            }
            if self.is_master() {
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
