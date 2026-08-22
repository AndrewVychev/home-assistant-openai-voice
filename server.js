import "dotenv/config";
import crypto from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";
import express from "express";
import WebSocket from "ws";
import { validateEntityAction } from "./lib/guard.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const app = express();

const config = {
  host: process.env.HOST || "127.0.0.1",
  port: Number(process.env.PORT || 3000),
  haUrl: (process.env.HA_URL || "http://homeassistant.local").replace(/\/$/, ""),
  model: process.env.OPENAI_REALTIME_MODEL || "gpt-realtime-2.1-mini",
  voice: process.env.OPENAI_VOICE || "marin",
};

const realtimePricing = {
  "gpt-realtime-2.1-mini": {
    currency: "USD",
    unit: "per_million_tokens",
    rates: {
      textInput: 0.6,
      textCached: 0.06,
      textOutput: 2.4,
      audioInput: 10,
      audioCached: 0.3,
      audioOutput: 20,
    },
  },
};

app.disable("x-powered-by");
app.use(express.json({ limit: "16kb" }));
app.use("/vendor/tfjs", express.static(path.join(__dirname, "node_modules/@tensorflow/tfjs/dist")));
app.use(
  "/vendor/speech-commands",
  express.static(path.join(__dirname, "node_modules/@tensorflow-models/speech-commands/dist")),
);
app.use(express.static(path.join(__dirname, "public")));

function requiredSecret(name) {
  const value = process.env[name]?.trim();
  if (!value || value.includes("replace-me") || value.startsWith("replace-with-")) {
    const error = new Error(`Добавьте ${name} в файл .env`);
    error.statusCode = 503;
    throw error;
  }
  return value;
}

async function fetchWithTimeout(url, options = {}, timeoutMs = 15_000) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  try {
    return await fetch(url, { ...options, signal: controller.signal });
  } finally {
    clearTimeout(timeout);
  }
}

async function haRequest(pathname, options = {}) {
  const token = requiredSecret("HA_TOKEN");
  return fetchWithTimeout(`${config.haUrl}${pathname}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...options.headers,
    },
  });
}

async function getHomeAssistantRegistries() {
  const token = requiredSecret("HA_TOKEN");
  const websocketUrl = new URL("/api/websocket", config.haUrl);
  websocketUrl.protocol = websocketUrl.protocol === "https:" ? "wss:" : "ws:";

  return new Promise((resolve, reject) => {
    const socket = new WebSocket(websocketUrl);
    const pending = new Map([
      [1, "entities"],
      [2, "devices"],
      [3, "areas"],
    ]);
    const result = {};
    let settled = false;
    const timeout = setTimeout(() => finish(new Error("Тайм-аут реестров Home Assistant.")), 8_000);

    function finish(error) {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      socket.close();
      if (error) reject(error);
      else resolve(result);
    }

    socket.on("error", finish);
    socket.on("message", (raw) => {
      const message = JSON.parse(raw.toString());
      if (message.type === "auth_required") {
        socket.send(JSON.stringify({ type: "auth", access_token: token }));
        return;
      }
      if (message.type === "auth_invalid") {
        finish(new Error("Home Assistant отклонил токен WebSocket API."));
        return;
      }
      if (message.type === "auth_ok") {
        socket.send(JSON.stringify({ id: 1, type: "config/entity_registry/list" }));
        socket.send(JSON.stringify({ id: 2, type: "config/device_registry/list" }));
        socket.send(JSON.stringify({ id: 3, type: "config/area_registry/list" }));
        return;
      }
      if (message.type === "result" && pending.has(message.id)) {
        if (!message.success) {
          finish(new Error(`Home Assistant не вернул реестр ${pending.get(message.id)}.`));
          return;
        }
        result[pending.get(message.id)] = message.result;
        pending.delete(message.id);
        if (pending.size === 0) finish();
      }
    });
  });
}

async function getAreaLookup() {
  const { entities, devices, areas } = await getHomeAssistantRegistries();
  const deviceById = new Map(devices.map((device) => [device.id, device]));
  const areaById = new Map(areas.map((area) => [area.area_id, area.name]));

  return new Map(
    entities.map((entity) => {
      const areaId = entity.area_id || deviceById.get(entity.device_id)?.area_id;
      return [entity.entity_id, areaId ? areaById.get(areaId) || areaId : null];
    }),
  );
}

async function getControllableEntities() {
  const [response, areaLookup] = await Promise.all([
    haRequest("/api/states"),
    getAreaLookup().catch((error) => {
      console.warn(`Комнаты Home Assistant недоступны: ${error.message}`);
      return new Map();
    }),
  ]);
  if (!response.ok) throw new Error(`Не удалось получить сущности HA: ${response.status}`);
  const states = await response.json();
  return states
    .filter(
      (entity) =>
        ["light", "switch", "climate"].includes(entity.entity_id.split(".")[0]) &&
        !entity.entity_id.startsWith("switch.shelly"),
    )
    .sort((left, right) => left.entity_id.localeCompare(right.entity_id))
    .slice(0, 100)
    .map((entity) => ({
      entity_id: entity.entity_id,
      name: entity.attributes?.friendly_name || entity.entity_id,
      area: areaLookup.get(entity.entity_id) || null,
    }));
}

app.get("/api/health", async (_req, res) => {
  const result = {
    ok: true,
    homeAssistant: { url: config.haUrl, reachable: false, authenticated: false },
    openAI: { configured: Boolean(process.env.OPENAI_API_KEY?.trim()) },
    model: config.model,
    pricing: realtimePricing[config.model] || null,
  };

  try {
    const headers = {};
    if (process.env.HA_TOKEN?.trim()) {
      headers.Authorization = `Bearer ${process.env.HA_TOKEN.trim()}`;
    }
    const response = await fetchWithTimeout(`${config.haUrl}/api/`, { headers }, 5_000);
    result.homeAssistant.reachable = true;
    result.homeAssistant.authenticated = response.ok;
    result.ok = response.ok && result.openAI.configured;
  } catch {
    result.ok = false;
  }

  res.status(result.ok ? 200 : 503).json(result);
});

app.post(
  "/session",
  express.text({ type: ["application/sdp", "text/plain"], limit: "128kb" }),
  async (req, res, next) => {
    try {
      const apiKey = requiredSecret("OPENAI_API_KEY");
      const entities = await getControllableEntities();
      const responseMode = req.query.response_mode === "text" ? "text" : "audio";
      if (!req.body?.startsWith("v=")) {
        return res.status(400).json({ error: "Ожидался WebRTC SDP offer." });
      }

      const session = {
        type: "realtime",
        model: config.model,
        instructions: [
          "Ты голосовой ассистент умного дома.",
          "Всегда отвечай по-русски, кратко и естественно.",
          "Для управления домом используй только control_home_entity и get_home_state.",
          "Не утверждай, что действие выполнено, пока функция не вернула успешный результат.",
          "Не придумывай состояния устройств. Если команда неоднозначна, сначала уточни.",
          "Для turn_on, turn_off и toggle сразу вызывай control_home_entity ровно один раз для выбранной сущности.",
          "Никогда не вызывай get_home_state до или после команды управления. get_home_state разрешён только когда пользователь явно спрашивает текущее состояние.",
          "Не произноси и не пиши план перед вызовом функции. Сначала молча вызови функцию.",
          `После успешной команды ${responseMode === "text" ? "напиши в чат" : "скажи"} одну фразу максимум из пяти слов, например: Свет на кухне включён.`,
          "После итогового ответа не вызывай другие функции, пока пользователь не произнесёт новую команду.",
          "Выбирай устройство по указанной комнате Home Assistant. Названия комнат могут быть на английском: кухня соответствует Kitchen, спальня — Bedroom, гостиная — Living Room.",
          "Если пользователь просит включить свет в комнате, выбери сущность домена light, у которой явно указана эта комната.",
          "Если подходящей сущности или разрешённого действия нет, сообщи об этом; не пытайся выполнить команду другим способом.",
          "После результата функции кратко сообщи пользователю итог.",
          `Доступные сущности: ${entities.map((entity) => `${entity.entity_id} (${entity.name}, комната: ${entity.area || "не назначена"})`).join("; ")}.`,
        ].join(" "),
        output_modalities: [responseMode],
        ...(responseMode === "audio" ? { audio: { output: { voice: config.voice } } } : {}),
        max_output_tokens: 180,
        tools: [
          {
            type: "function",
            name: "control_home_entity",
            description:
              "Сразу управляет конкретной разрешённой сущностью Home Assistant. Для turn_on, turn_off или toggle вызывай его без предварительного и последующего get_home_state.",
            parameters: {
              type: "object",
              properties: {
                entity_id: {
                  type: "string",
                  enum: entities.map((entity) => entity.entity_id),
                  description: "Точный entity_id из списка доступных сущностей.",
                },
                action: {
                  type: "string",
                  enum: ["turn_on", "turn_off", "toggle", "set_temperature"],
                },
                temperature: {
                  type: "number",
                  description: "Температура 10–30 °C; только для set_temperature.",
                },
              },
              required: ["entity_id", "action"],
              additionalProperties: false,
            },
          },
          {
            type: "function",
            name: "get_home_state",
            description:
              "Получает состояние только когда пользователь явно спрашивает, включено ли устройство или каково его состояние. Не используй перед или после управления.",
            parameters: {
              type: "object",
              properties: {
                entity_id: {
                  type: "string",
                  enum: entities.map((entity) => entity.entity_id),
                },
              },
              required: ["entity_id"],
              additionalProperties: false,
            },
          },
        ],
        tool_choice: "auto",
      };

      const form = new FormData();
      form.set("sdp", req.body);
      form.set("session", JSON.stringify(session));

      const response = await fetchWithTimeout(
        "https://api.openai.com/v1/realtime/calls",
        {
          method: "POST",
          headers: {
            Authorization: `Bearer ${apiKey}`,
            "OpenAI-Safety-Identifier": crypto
              .createHash("sha256")
              .update("local-home-assistant-prototype")
              .digest("hex"),
          },
          body: form,
        },
        30_000,
      );

      const body = await response.text();
      if (!response.ok) {
        console.error("OpenAI session error", response.status, body.slice(0, 500));
        return res.status(response.status).json({
          error: "OpenAI не создал Realtime-сессию.",
          details: body.slice(0, 500),
        });
      }

      res.type("application/sdp").send(body);
    } catch (error) {
      next(error);
    }
  },
);

app.post("/api/ha/entity", async (req, res, next) => {
  try {
    const requestedAction = req.body?.action || "get_state";
    const validation = validateEntityAction({
      entityId: req.body?.entity_id,
      action: requestedAction,
      temperature: req.body?.temperature,
    });
    if (!validation.ok) {
      return res.status(403).json({ ok: false, error: validation.reason });
    }

    if (validation.action === "get_state") {
      const response = await haRequest(`/api/states/${validation.entityId}`);
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {
        return res.status(response.status).json({ ok: false, error: `Home Assistant ответил ${response.status}.` });
      }
      return res.json({
        ok: true,
        entityId: validation.entityId,
        name: data.attributes?.friendly_name || validation.entityId,
        state: data.state,
        temperature: data.attributes?.current_temperature,
      });
    }

    const serviceData = { entity_id: validation.entityId };
    if (validation.action === "set_temperature") {
      serviceData.temperature = validation.temperature;
    }
    const response = await haRequest(
      `/api/services/${validation.domain}/${validation.action}`,
      { method: "POST", body: JSON.stringify(serviceData) },
    );
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      return res.status(response.status).json({
        ok: false,
        error: `Home Assistant ответил ${response.status}.`,
        details: data,
      });
    }

    const stateResponse = await haRequest(`/api/states/${validation.entityId}`);
    const state = await stateResponse.json().catch(() => ({}));
    return res.json({
      ok: true,
      entityId: validation.entityId,
      action: validation.action,
      state: state.state,
      name: state.attributes?.friendly_name || validation.entityId,
    });
  } catch (error) {
    next(error);
  }
});

app.use((error, _req, res, _next) => {
  const status = error.statusCode || (error.name === "AbortError" ? 504 : 500);
  console.error(error);
  res.status(status).json({ error: error.message || "Внутренняя ошибка." });
});

app.listen(config.port, config.host, () => {
  console.log(`Voice prototype: http://${config.host}:${config.port}`);
  console.log(`Home Assistant: ${config.haUrl}`);
  console.log(`Realtime model: ${config.model}`);
});
