use std::fs;
use std::io::Read;
use std::path::PathBuf;

use eframe::egui;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::Value;

const VERSION: &str = env!("CARGO_PKG_VERSION");

#[derive(Clone, Debug, Deserialize)]
struct Account {
    #[allow(dead_code)]
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

#[derive(Deserialize)]
struct Me {
    #[allow(dead_code)]
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
        self.campaigns = me.campaigns;
        self.screen = Screen::Campaigns;
        Ok(())
    }

    fn open_campaign(&mut self, campaign: Campaign) {
        self.campaign = Some(campaign.clone());
        self.scenes.clear();
        self.scene = None;
        self.texture = None;
        self.pending_image = None;
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

    fn open_scene(&mut self, summary: SceneSummary) {
        let campaign = match &self.campaign {
            Some(campaign) => campaign.clone(),
            None => return,
        };
        self.error.clear();
        let scene: Scene = match self.get_json(&format!(
            "/v1/campaigns/{}/scenes/{}",
            campaign.id, summary.id
        )) {
            Ok(scene) => scene,
            Err(error) => {
                self.error = error;
                return;
            }
        };
        match self.fetch_image(&campaign.id, &scene.image) {
            Ok(data) => {
                self.pending_image = Some(data);
                self.zoom = 0.0;
                self.offset = egui::Vec2::ZERO;
                self.scene = Some(scene);
                self.screen = Screen::Scene;
            }
            Err(error) => self.error = error,
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
            ui.label("кампаний нет или они не загрузились");
        }
        let items = self.campaigns.clone();
        for campaign in items {
            if ui
                .button(format!("{}  ({})", campaign.name, campaign.role))
                .clicked()
            {
                self.open_campaign(campaign);
            }
        }
    }

    fn show_scenes(&mut self, ui: &mut egui::Ui) {
        if ui.button("← кампании").clicked() {
            self.screen = Screen::Campaigns;
        }
        if let Some(campaign) = &self.campaign {
            ui.heading(&campaign.name);
        }
        if self.scenes.is_empty() {
            ui.label("сцен нет: импортируйте карту через API");
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
                ui.heading("Импорт карты");
                ui.label("картинка");
                ui.text_edit_singleline(&mut self.image_path);
                ui.label("annotation.json");
                ui.text_edit_singleline(&mut self.annotation_path);
                ui.label("имя сцены");
                ui.text_edit_singleline(&mut self.scene_name);
                if ui.button("Импортировать").clicked() {
                    self.import_scene();
                }
            }
        }
    }
}

impl App {
    fn show_scene(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            if ui.button("← сцены").clicked() {
                self.screen = Screen::Scenes;
            }
            if let Some(scene) = &self.scene {
                ui.heading(&scene.name);
            }
        });
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
        if response.dragged() {
            self.offset += response.drag_delta();
        }
        let scroll = ui.input(|input| input.smooth_scroll_delta.y);
        if scroll.abs() > 0.0 {
            let factor = if scroll > 0.0 { 1.1 } else { 1.0 / 1.1 };
            self.zoom = (self.zoom * factor).clamp(0.02, 12.0);
        }
        let size = egui::vec2(image_w * self.zoom, image_h * self.zoom);
        let origin = rect.center() - size / 2.0 + self.offset;
        let map_rect = egui::Rect::from_min_size(origin, size);
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
                painter.line_segment([*from, *to], stroke_green);
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
    }
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
}

impl eframe::App for App {
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        let ctx = ui.ctx().clone();
        self.make_texture(&ctx);
        let account_login = self.account.as_ref().map(|account| account.login.clone());
        let base_url = self.base_url.clone();
        let logged = self.token.is_some();
        let mut logout = false;
        egui::Panel::top("top").show(ui, |ui| {
            ui.horizontal(|ui| {
                ui.heading("VTT");
                if let Some(login) = &account_login {
                    ui.separator();
                    ui.label(login);
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
}
