use std::collections::HashMap;
use std::fs;
use std::io::Read;
use std::path::PathBuf;
use std::sync::mpsc::{self, Receiver};
use std::thread;

use eframe::egui;
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

enum ChatEvent {
    Connected,
    Message(String),
    Tokens(Vec<Token>),
    TokenPlaced(Token),
    TokenMoved(Token),
    TokenUpdated(Token),
    SceneShown(SceneSummary, Vec<Token>, HashMap<String, bool>),
    Doors(HashMap<String, bool>),
    DoorSet(String, bool),
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
}

#[derive(Clone, Copy, PartialEq)]
enum Screen {
    Login,
    Campaigns,
    Scenes,
    Scene,
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
    turn_token: Option<String>,
    round: u32,
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
            turn_token: None,
            round: 0,
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
        ui.add_space(40.0);
        ui.vertical_centered(|ui| {
            ui.heading("VTT");
            ui.label("вход в кампанию");
            ui.add_space(12.0);
            ui.set_max_width(360.0);
            ui.label("сервер");
            ui.text_edit_singleline(&mut self.base_url);
            ui.label("логин");
            ui.text_edit_singleline(&mut self.login);
            ui.label("пароль");
            ui.add(egui::TextEdit::singleline(&mut self.password).password(true));
            ui.add_space(8.0);
            if ui.button("Войти").clicked() {
                self.login();
            }
            if !self.error.is_empty() {
                ui.colored_label(egui::Color32::LIGHT_RED, &self.error);
            }
        });
    }

    fn show_campaigns(&mut self, ui: &mut egui::Ui) {
        ui.heading("Кампании");
        if self.campaigns.is_empty() {
            ui.label("пока пусто — создайте кампанию или войдите по коду");
        }
        let items = self.campaigns.clone();
        for campaign in items {
            if ui
                .button(format!(
                    "{}  ·  {}",
                    campaign.name,
                    role_name(&campaign.role)
                ))
                .clicked()
            {
                self.open_campaign(campaign);
            }
        }
        ui.separator();
        ui.heading("Новая кампания");
        ui.horizontal(|ui| {
            ui.add(
                egui::TextEdit::singleline(&mut self.new_campaign)
                    .hint_text("название")
                    .desired_width(260.0),
            );
            if ui.button("Создать").clicked() {
                self.create_campaign();
            }
        });
        ui.separator();
        ui.heading("Войти по коду");
        ui.horizontal(|ui| {
            ui.add(
                egui::TextEdit::singleline(&mut self.join_code)
                    .hint_text("код от мастера")
                    .desired_width(180.0),
            );
            if ui.button("Войти").clicked() {
                self.accept_invite();
            }
        });
    }

    fn show_scenes(&mut self, ui: &mut egui::Ui) {
        if ui.button("← кампании").clicked() {
            self.screen = Screen::Campaigns;
        }
        if let Some(campaign) = &self.campaign {
            ui.heading(&campaign.name);
            if campaign.role == "owner" {
                if ui.button("Пригласить игрока").clicked() {
                    self.create_invite();
                }
                if !self.invite_code.is_empty() {
                    ui.label(format!("код: {}", self.invite_code));
                }
            }
        }
        if let Some(detail) = &self.campaign_detail {
            ui.label("участники:");
            for member in &detail.members {
                ui.label(format!("  {} — {}", member.login, member.role));
            }
            ui.separator();
        }
        if self.scenes.is_empty() {
            ui.label("сцен нет — откройте «Импорт карты» ниже");
        }
        let items = self.scenes.clone();
        for scene in items {
            let mark = if scene.active {
                " · активна"
            } else {
                ""
            };
            if ui.button(format!("{}{}", scene.name, mark)).clicked() {
                self.open_scene(scene);
            }
        }
        if let Some(campaign) = &self.campaign {
            if campaign.role == "owner" || campaign.role == "master" {
                ui.separator();
                egui::CollapsingHeader::new("Импорт карты")
                    .default_open(self.scenes.is_empty())
                    .show(ui, |ui| {
                        ui.label("перетащите в окно картинку и annotation.json — или впишите пути");
                        ui.label(format!("картинка: {}", file_name(&self.image_path)));
                        ui.text_edit_singleline(&mut self.image_path);
                        ui.label(format!(
                            "annotation.json: {}",
                            file_name(&self.annotation_path)
                        ));
                        ui.text_edit_singleline(&mut self.annotation_path);
                        ui.label("имя сцены");
                        ui.text_edit_singleline(&mut self.scene_name);
                        if ui.button("Импортировать").clicked() {
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
            if ui.button("← сцены").clicked() {
                self.screen = Screen::Scenes;
            }
            if let Some(scene) = &self.scene {
                ui.heading(&scene.name);
            }
            if let Some(campaign) = &self.campaign {
                if campaign.role == "owner" || campaign.role == "master" {
                    if ui.button("Показать всем").clicked() {
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
        let (response, painter) = ui.allocate_painter(ui.available_size(), egui::Sense::drag());
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
        if response.drag_started() {
            if let Some(position) = pointer {
                self.drag_token = self
                    .tokens
                    .iter()
                    .find(|token| {
                        let center = egui::pos2(
                            origin.x + token.x as f32 * self.zoom,
                            origin.y + token.y as f32 * self.zoom,
                        );
                        center.distance(position) <= 16.0 && self.can_move_token(token)
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
        let stroke_grid =
            egui::Stroke::new(1.0, egui::Color32::from_rgba_unmultiplied(255, 90, 90, 140));

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
        for token in &self.tokens {
            let center = to_screen(token.x as f32, token.y as f32);
            let fill = token_color(&token.color);
            let radius = 13.0;
            painter.circle_filled(center, radius, fill);
            painter.circle_stroke(
                center,
                radius,
                egui::Stroke::new(2.0, egui::Color32::from_rgb(30, 30, 30)),
            );
            let label = center + egui::vec2(0.0, radius + 9.0);
            painter.text(
                label + egui::vec2(1.0, 1.0),
                egui::Align2::CENTER_CENTER,
                &token.name,
                egui::FontId::proportional(12.0),
                egui::Color32::BLACK,
            );
            painter.text(
                label,
                egui::Align2::CENTER_CENTER,
                &token.name,
                egui::FontId::proportional(12.0),
                egui::Color32::WHITE,
            );
        }
    }
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
                ChatEvent::Doors(doors) => self.doors = doors,
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

    fn move_token(&self, token_id: &str, x: f64, y: f64) {
        self.send_intent(serde_json::json!({
            "type": "token.move",
            "token_id": token_id,
            "x": x,
            "y": y,
        }));
    }

    fn show_chat(&mut self, ui: &mut egui::Ui) {
        ui.heading("Стол");
        ui.label(&self.chat_status);
        ui.separator();
        egui::ScrollArea::vertical()
            .stick_to_bottom(true)
            .auto_shrink([false, false])
            .max_height(180.0)
            .show(ui, |ui| {
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
        if let Some(campaign) = &self.campaign {
            if campaign.role == "owner" || campaign.role == "master" {
                ui.separator();
                ui.heading("Фишки");
                ui.add(
                    egui::TextEdit::singleline(&mut self.token_name)
                        .hint_text("имя фишки")
                        .desired_width(200.0),
                );
                let members = self
                    .campaign_detail
                    .as_ref()
                    .map(|detail| detail.members.clone())
                    .unwrap_or_default();
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

impl eframe::App for App {
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        let ctx = ui.ctx().clone();
        ctx.request_repaint_after(std::time::Duration::from_millis(250));
        self.accept_dropped_files(&ctx);
        self.make_texture(&ctx);
        self.poll_chat();
        let account_login = self.account.as_ref().map(|account| account.login.clone());
        let role_label = self
            .campaign
            .as_ref()
            .map(|campaign| role_name(&campaign.role));
        let base_url = self.base_url.clone();
        let logged = self.token.is_some();
        let mut logout = false;
        egui::Panel::top("top").show(ui, |ui| {
            ui.horizontal(|ui| {
                ui.heading("VTT");
                if let Some(login) = &account_login {
                    ui.separator();
                    match role_label {
                        Some(role) => ui.label(format!("{login} · {role}")),
                        None => ui.label(login),
                    };
                }
                if !base_url.is_empty() {
                    ui.separator();
                    ui.label(&base_url);
                }
                if logged && ui.button("Выйти").clicked() {
                    logout = true;
                }
            });
        });
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
                .default_size(340.0)
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
        viewport: egui::ViewportBuilder::default().with_inner_size([1280.0, 860.0]),
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
}
