import "dotenv/config";
import { createServer } from "node:http";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { GoogleGenAI, Modality } from "@google/genai";
import express from "express";
import WebSocket, { WebSocketServer } from "ws";
import { validateEntityAction } from "./lib/guard.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const app = express();

const config = {
  host: process.env.HOST || "127.0.0.1",
  port: Number(process.env.PORT || 3000),
  haUrl: (process.env.HA_URL || "http://homeassistant.local").replace(/\/$/, ""),
  model: process.env.GEMINI_LIVE_MODEL || "gemini-3.1-flash-live-preview",
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

function buildInstructions(entities, responseMode) {
  return [
    "Ты голосовой ассистент умного дома.",
    "Всегда отвечай по-русски.",
    "КРИТИЧЕСКОЕ ПРАВИЛО: любой твой голосовой ответ должен содержать не больше пяти слов.",
    "Никаких приветствий, объяснений, рассуждений, пересказа команды, планов, советов и дополнительных вопросов.",
    "После успешного действия скажи только краткий итог, например: «Свет включён» или «Свет выключен».",
    "Если команда неоднозначна, задай ровно один короткий вопрос с вариантами и затем молчи.",
    "Для управления домом используй только control_home_entity и get_home_state.",
    "Не утверждай, что действие выполнено, пока функция не вернула успешный результат.",
    "Не придумывай состояния устройств. Если команда неоднозначна, сначала уточни и жди ответа пользователя.",
    "Для turn_on, turn_off и toggle сразу вызывай control_home_entity ровно один раз для выбранной сущности.",
    "Не вызывай get_home_state до или после управления. При любом явном вопросе о текущем состоянии устройства обязан вызвать get_home_state и не имеешь права отвечать по памяти.",
    "Не сообщай план перед вызовом функции.",
    `После успешной команды ${responseMode === "text" ? "напиши" : "скажи"} итог максимум в пяти словах.`,
    "Выбирай устройство по комнате Home Assistant. Кухня соответствует Kitchen, спальня — Bedroom, гостиная — Living Room.",
    "Если пользователь просит свет в комнате, выбирай сущность light с этой комнатой.",
    "Если подходящей сущности нет, сообщи об этом и не выполняй другое действие.",
    `Доступные сущности: ${entities.map((entity) => `${entity.entity_id} (${entity.name}, комната: ${entity.area || "не назначена"})`).join("; ")}.`,
  ].join(" ");
}

function buildTools(entities) {
  return [{
    functionDeclarations: [
      {
        name: "control_home_entity",
        description: "Управляет одной разрешённой сущностью Home Assistant.",
        parametersJsonSchema: {
          type: "object",
          properties: {
            entity_id: { type: "string", enum: entities.map((entity) => entity.entity_id) },
            action: { type: "string", enum: ["turn_on", "turn_off", "toggle", "set_temperature"] },
            temperature: { type: "number", minimum: 10, maximum: 30 },
          },
          required: ["entity_id", "action"],
          additionalProperties: false,
        },
      },
      {
        name: "get_home_state",
        description: "Получает состояние сущности, когда пользователь явно спрашивает о нём.",
        parametersJsonSchema: {
          type: "object",
          properties: {
            entity_id: { type: "string", enum: entities.map((entity) => entity.entity_id) },
          },
          required: ["entity_id"],
          additionalProperties: false,
        },
      },
    ],
  }];
}

app.get("/api/health", async (_req, res) => {
  const result = {
    ok: true,
    homeAssistant: { url: config.haUrl, reachable: false, authenticated: false },
    google: { configured: Boolean(process.env.GEMINI_API_KEY?.trim()) },
    model: config.model,
    pricing: null,
  };

  try {
    const headers = {};
    if (process.env.HA_TOKEN?.trim()) {
      headers.Authorization = `Bearer ${process.env.HA_TOKEN.trim()}`;
    }
    const response = await fetchWithTimeout(`${config.haUrl}/api/`, { headers }, 5_000);
    result.homeAssistant.reachable = true;
    result.homeAssistant.authenticated = response.ok;
    result.ok = response.ok && result.google.configured;
  } catch {
    result.ok = false;
  }

  res.status(result.ok ? 200 : 503).json(result);
});

async function performHomeAction(body = {}) {
    const requestedAction = body.action || "get_state";
    const validation = validateEntityAction({
      entityId: body.entity_id,
      action: requestedAction,
      temperature: body.temperature,
    });
    if (!validation.ok) {
      const error = new Error(validation.reason);
      error.statusCode = 403;
      throw error;
    }

    if (validation.action === "get_state") {
      const response = await haRequest(`/api/states/${validation.entityId}`);
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {
        const error = new Error(`Home Assistant ответил ${response.status}.`);
        error.statusCode = response.status;
        throw error;
      }
      return {
        ok: true,
        entityId: validation.entityId,
        name: data.attributes?.friendly_name || validation.entityId,
        state: data.state,
        temperature: data.attributes?.current_temperature,
      };
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
      const error = new Error(`Home Assistant ответил ${response.status}.`);
      error.statusCode = response.status;
      error.details = data;
      throw error;
    }

    const stateResponse = await haRequest(`/api/states/${validation.entityId}`);
    const state = await stateResponse.json().catch(() => ({}));
    return {
      ok: true,
      entityId: validation.entityId,
      action: validation.action,
      state: state.state,
      name: state.attributes?.friendly_name || validation.entityId,
    };
}

app.post("/api/ha/entity", async (req, res, next) => {
  try {
    res.json(await performHomeAction(req.body));
  } catch (error) {
    next(error);
  }
});

const server = createServer(app);
const liveSockets = new WebSocketServer({ noServer: true });

function sendJson(socket, payload) {
  if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(payload));
}

function relayGeminiMessage(socket, message) {
  const content = message.serverContent;
  if (content?.inputTranscription?.text) {
    sendJson(socket, { type: "input_transcript", text: content.inputTranscription.text });
  }
  if (content?.outputTranscription?.text) {
    sendJson(socket, { type: "output_transcript", text: content.outputTranscription.text });
  }
  for (const part of content?.modelTurn?.parts || []) {
    if (part.text) sendJson(socket, { type: "text_delta", text: part.text });
    if (part.inlineData?.data) {
      sendJson(socket, {
        type: "audio_delta",
        data: part.inlineData.data,
        mimeType: part.inlineData.mimeType || "audio/pcm;rate=24000",
      });
    }
  }
  if (content?.interrupted) sendJson(socket, { type: "interrupted" });
  if (content?.waitingForInput) sendJson(socket, { type: "waiting_for_input" });
  if (content?.turnComplete) {
    sendJson(socket, { type: "turn_complete", reason: content.turnCompleteReason || null });
  }
  if (message.usageMetadata) {
    sendJson(socket, { type: "usage", usage: message.usageMetadata });
  }
}

async function executeGeminiTools(session, socket, functionCalls = []) {
  const functionResponses = [];
  for (const call of functionCalls) {
    if (!["control_home_entity", "get_home_state"].includes(call.name)) continue;
    const args = call.args || {};
    const requestBody = {
      ...args,
      action: call.name === "get_home_state" ? "get_state" : args.action,
    };
    sendJson(socket, {
      type: "tool_call",
      name: call.name,
      args,
    });

    let result;
    try {
      result = await performHomeAction(requestBody);
    } catch (error) {
      result = { ok: false, error: error.message };
    }
    sendJson(socket, { type: "ha_result", result });
    functionResponses.push({
      id: call.id,
      name: call.name,
      response: result.ok ? { output: result } : { error: result.error },
    });
  }
  if (functionResponses.length) session.sendToolResponse({ functionResponses });
}

liveSockets.on("connection", async (socket, request) => {
  let session;
  let closed = false;
  socket.on("close", () => {
    closed = true;
    session?.close();
  });
  try {
    const apiKey = requiredSecret("GEMINI_API_KEY");
    const responseMode = "audio";
    const entities = await getControllableEntities();
    const ai = new GoogleGenAI({ apiKey });

    session = await ai.live.connect({
      model: config.model,
      config: {
        responseModalities: [Modality.AUDIO],
        systemInstruction: buildInstructions(entities, responseMode),
        inputAudioTranscription: {},
        outputAudioTranscription: {},
        maxOutputTokens: 64,
        thinkingConfig: {
          thinkingLevel: "minimal",
          includeThoughts: false,
        },
        tools: buildTools(entities),
      },
      callbacks: {
        onopen: () => {},
        onmessage: (message) => {
          relayGeminiMessage(socket, message);
          if (message.toolCall?.functionCalls?.length) {
            executeGeminiTools(session, socket, message.toolCall.functionCalls).catch((error) => {
              sendJson(socket, { type: "error", message: error.message });
            });
          }
        },
        onerror: (event) => {
          const message = event?.error?.message || event?.message || "Ошибка Gemini Live API";
          console.error("Gemini Live error", message);
          sendJson(socket, { type: "error", message });
        },
        onclose: (event) => {
          const reason = event?.reason || "Gemini закрыл соединение";
          if (event?.code && event.code !== 1000) {
            console.warn("Gemini Live closed", event.code, reason);
          }
          sendJson(socket, { type: "closed", code: event?.code || null, reason });
          if (socket.readyState === WebSocket.OPEN) socket.close();
        },
      },
    });

    if (closed) {
      session.close();
      return;
    }
    sendJson(socket, { type: "ready", model: config.model, responseMode });

    socket.on("message", (raw) => {
      try {
        const event = JSON.parse(raw.toString());
        if (event.type === "audio" && event.data) {
          session.sendRealtimeInput({
            audio: { data: event.data, mimeType: "audio/pcm;rate=16000" },
          });
        } else if (event.type === "audio_stream_end") {
          session.sendRealtimeInput({ audioStreamEnd: true });
        } else if (event.type === "text" && event.text) {
          session.sendClientContent({
            turns: [{ role: "user", parts: [{ text: event.text }] }],
            turnComplete: true,
          });
        }
      } catch (error) {
        sendJson(socket, { type: "error", message: `Некорректное сообщение клиента: ${error.message}` });
      }
    });
  } catch (error) {
    sendJson(socket, { type: "error", message: error.message });
    socket.close(1011, "Gemini session failed");
  }

});

server.on("upgrade", (request, socket, head) => {
  const url = new URL(request.url, `http://${request.headers.host || "localhost"}`);
  if (url.pathname !== "/gemini-live") {
    socket.destroy();
    return;
  }
  liveSockets.handleUpgrade(request, socket, head, (websocket) => {
    liveSockets.emit("connection", websocket, request);
  });
});

app.use((error, _req, res, _next) => {
  const status = error.statusCode || (error.name === "AbortError" ? 504 : 500);
  console.error(error);
  res.status(status).json({ error: error.message || "Внутренняя ошибка." });
});

server.listen(config.port, config.host, () => {
  console.log(`Voice prototype: http://${config.host}:${config.port}`);
  console.log(`Home Assistant: ${config.haUrl}`);
  console.log(`Gemini Live model: ${config.model}`);
});
