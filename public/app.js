import { addRealtimeUsage, calculateRealtimeCost, createUsage } from "./usage.js";

const connectButton = document.querySelector("#connect");
const buttonLabel = document.querySelector("#button-label");
const muteButton = document.querySelector("#mute");
const wakeButton = document.querySelector("#wake");
const clearButton = document.querySelector("#clear");
const statusText = document.querySelector("#status");
const statusDot = document.querySelector("#status-dot");
const messages = document.querySelector("#messages");
const wakeTraining = document.querySelector("#wake-training");
const recordWakeButton = document.querySelector("#record-wake");
const recordNoiseButton = document.querySelector("#record-noise");
const trainWakeButton = document.querySelector("#train-wake");
const wakeCount = document.querySelector("#wake-count");
const noiseCount = document.querySelector("#noise-count");
const trainingStatus = document.querySelector("#training-status");
const lastCost = document.querySelector("#last-cost");
const totalCost = document.querySelector("#total-cost");
const usageDetails = document.querySelector("#usage-details");
const responseModeSelect = document.querySelector("#response-mode");

let peerConnection;
let dataChannel;
let microphoneStream;
let remoteAudio;
let muted = false;
let connecting = false;
let baseWakeRecognizer;
let wakeRecognizer;
let wakeEnabled = false;
let wakeListening = false;
let wakeTriggered = false;
let wakeSession = false;
let wakeModelReady = false;
let wakeRestartTimer;
let wakeSessionTimer;
let pricingRates;
let requestUsage = createUsage();
let requestUsageActive = false;
let pageCost = 0;
let sessionResponseMode = "text";
let turnToolExecuted = false;

const WAKE_MODEL_NAME = "home-wake-sho-ty-golova-v1";
const WAKE_LABEL = "sho-ty-golova";
const NOISE_LABEL = "_background_noise_";
const REQUIRED_EXAMPLES = 8;

function setStatus(text, kind = "") {
  statusText.textContent = text;
  statusDot.className = `status-dot ${kind}`.trim();
}

function addMessage(title, text, kind = "") {
  const item = document.createElement("div");
  item.className = `message ${kind}`.trim();
  const strong = document.createElement("strong");
  strong.textContent = `${title}: `;
  item.append(strong, document.createTextNode(text));
  messages.append(item);
  messages.scrollTop = messages.scrollHeight;
}

function formatUsd(value) {
  if (!Number.isFinite(value)) return "—";
  return `$${value.toFixed(value < 0.01 ? 6 : 4)}`;
}

function formatTokens(value) {
  return new Intl.NumberFormat("ru-RU").format(value || 0);
}

function beginRequestUsage() {
  if (requestUsageActive) return;
  requestUsage = createUsage();
  requestUsageActive = true;
  turnToolExecuted = false;
}

function finishRequestUsage() {
  if (!requestUsageActive) return;
  const cost = calculateRealtimeCost(requestUsage, pricingRates);
  if (cost) {
    pageCost += cost.total;
    lastCost.textContent = formatUsd(cost.total);
    totalCost.textContent = formatUsd(pageCost);
  } else {
    lastCost.textContent = "тариф неизвестен";
  }
  const cacheRate = requestUsage.inputTokens > 0
    ? Math.round((requestUsage.cachedTokens / requestUsage.inputTokens) * 100)
    : 0;
  usageDetails.textContent = [
    `вход ${formatTokens(requestUsage.inputTokens)}`,
    `кэш ${formatTokens(requestUsage.cachedTokens)} (${cacheRate}%)`,
    `выход ${formatTokens(requestUsage.outputTokens)}`,
    `аудио ${formatTokens(requestUsage.inputAudioTokens)} → ${formatTokens(requestUsage.outputAudioTokens)}`,
  ].join(" · ");
  console.info("OpenAI Realtime request usage", {
    model: statusText.dataset.model,
    usage: requestUsage,
    cost,
  });
  requestUsageActive = false;
}

function recordResponseUsage(event) {
  const response = event.response || {};
  if (response.usage) {
    beginRequestUsage();
    requestUsage = addRealtimeUsage(requestUsage, response.usage);
  }
  const hasFunctionCall = response.output?.some((item) => item.type === "function_call");
  if (!hasFunctionCall) finishRequestUsage();
}

function getResponseText(response) {
  return (response?.output || [])
    .filter((item) => item.type === "message")
    .flatMap((item) => item.content || [])
    .filter((part) => part.type === "output_text" && part.text)
    .map((part) => part.text.trim())
    .filter(Boolean)
    .join(" ");
}

function playWakeChime() {
  const AudioContextApi = window.AudioContext || window.webkitAudioContext;
  if (!AudioContextApi) return;
  const context = new AudioContextApi();
  const gain = context.createGain();
  gain.gain.setValueAtTime(0.0001, context.currentTime);
  gain.gain.exponentialRampToValueAtTime(0.12, context.currentTime + 0.02);
  gain.gain.exponentialRampToValueAtTime(0.0001, context.currentTime + 0.28);
  gain.connect(context.destination);
  [660, 880].forEach((frequency, index) => {
    const oscillator = context.createOscillator();
    oscillator.frequency.value = frequency;
    oscillator.connect(gain);
    oscillator.start(context.currentTime + index * 0.08);
    oscillator.stop(context.currentTime + 0.3);
  });
  setTimeout(() => context.close(), 500);
}

function scheduleWakeRestart(delay = 500) {
  clearTimeout(wakeRestartTimer);
  if (!wakeEnabled || !wakeModelReady || peerConnection || connecting || wakeListening) return;
  wakeRestartTimer = setTimeout(async () => {
    if (!wakeEnabled || peerConnection || connecting || wakeListening) return;
    try {
      await startWakeListening();
    } catch (error) {
      console.error("Wake word restart failed", error);
      setStatus(`Wake word: ${error.message}`, "error");
    }
  }, delay);
}

function scheduleSessionTimeout(delay = 12_000) {
  clearTimeout(wakeSessionTimer);
  wakeSessionTimer = setTimeout(() => {
    if (peerConnection) stopSession(true);
  }, delay);
}

async function triggerWakeWord() {
  if (wakeTriggered || peerConnection || connecting) return;
  wakeTriggered = true;
  wakeSession = true;
  await stopWakeListening();
  setStatus("Фраза услышана · подключаюсь…", "ok");
  addMessage("Система", "Услышал «Шо ты голова».");
  playWakeChime();
  await startSession();
  wakeTriggered = false;
  if (!peerConnection) {
    wakeSession = false;
    scheduleWakeRestart(1_000);
  }
}

async function initializeWakeModel() {
  if (wakeRecognizer) return;
  if (!window.tf || !window.speechCommands) {
    throw new Error("TensorFlow.js не загрузился.");
  }
  setStatus("Загружаю TensorFlow-модель…");
  await window.tf.ready();
  baseWakeRecognizer = window.speechCommands.create("BROWSER_FFT");
  await baseWakeRecognizer.ensureModelLoaded();
  wakeRecognizer = baseWakeRecognizer.createTransfer(WAKE_MODEL_NAME);

  const savedModels = await window.speechCommands.listSavedTransferModels();
  if (savedModels.includes(WAKE_MODEL_NAME)) {
    await wakeRecognizer.load();
    wakeModelReady = true;
  }
}

function exampleCounts() {
  if (!wakeRecognizer || wakeRecognizer.isDatasetEmpty()) return {};
  return wakeRecognizer.countExamples();
}

function updateTrainingUi() {
  const counts = exampleCounts();
  const positives = counts[WAKE_LABEL] || 0;
  const negatives = counts[NOISE_LABEL] || 0;
  wakeCount.textContent = String(positives);
  noiseCount.textContent = String(negatives);
  trainWakeButton.disabled = positives < REQUIRED_EXAMPLES || negatives < REQUIRED_EXAMPLES;
}

async function collectWakeExample(label) {
  recordWakeButton.disabled = true;
  recordNoiseButton.disabled = true;
  trainWakeButton.disabled = true;
  trainingStatus.textContent = label === WAKE_LABEL
    ? "Говори сейчас: «Шо ты голова»"
    : "Помолчи секунду — записываю фон";
  try {
    await initializeWakeModel();
    await wakeRecognizer.collectExample(label);
    updateTrainingUi();
    trainingStatus.textContent = "Пример записан. Запиши ещё несколько с разной интонацией.";
  } catch (error) {
    trainingStatus.textContent = `Ошибка записи: ${error.message}`;
  } finally {
    recordWakeButton.disabled = false;
    recordNoiseButton.disabled = false;
    updateTrainingUi();
  }
}

async function trainWakeModel() {
  trainWakeButton.disabled = true;
  recordWakeButton.disabled = true;
  recordNoiseButton.disabled = true;
  try {
    await wakeRecognizer.train({
      epochs: 30,
      validationSplit: 0.2,
      batchSize: 8,
      callback: {
        onEpochEnd: async (epoch, logs) => {
          const accuracy = Number(logs.acc ?? logs.accuracy ?? 0);
          trainingStatus.textContent = `Обучение ${epoch + 1}/30 · точность ${(accuracy * 100).toFixed(0)}%`;
          await window.tf.nextFrame();
        },
      },
    });
    await wakeRecognizer.save();
    wakeModelReady = true;
    wakeTraining.hidden = true;
    addMessage("Wake word", "TensorFlow-модель обучена и сохранена в этом браузере.");
    await enableWakeWord();
  } catch (error) {
    trainingStatus.textContent = `Ошибка обучения: ${error.message}`;
    addMessage("Wake word", error.message, "error");
  } finally {
    recordWakeButton.disabled = false;
    recordNoiseButton.disabled = false;
    updateTrainingUi();
  }
}

async function startWakeListening() {
  if (wakeListening || !wakeModelReady) return;
  wakeListening = true;
  try {
    await wakeRecognizer.listen((result) => {
      const labels = wakeRecognizer.wordLabels();
      const wakeIndex = labels.indexOf(WAKE_LABEL);
      if (wakeIndex >= 0 && result.scores[wakeIndex] >= 0.86) triggerWakeWord();
    }, {
      probabilityThreshold: 0.86,
      overlapFactor: 0.5,
      suppressionTimeMillis: 1_500,
    });
    setStatus("Жду: «Шо ты голова» · TensorFlow локально", "ok");
  } catch (error) {
    wakeListening = false;
    throw error;
  }
}

async function stopWakeListening() {
  if (!wakeListening || !wakeRecognizer) return;
  wakeListening = false;
  try {
    await wakeRecognizer.stopListening();
  } catch (error) {
    console.warn("Wake listener stop failed", error);
  }
}

async function enableWakeWord() {
  wakeButton.disabled = true;
  try {
    await initializeWakeModel();
    if (!wakeModelReady) {
      wakeTraining.hidden = false;
      updateTrainingUi();
      trainingStatus.textContent = "Запиши минимум 8 фраз и 8 примеров фонового шума.";
      setStatus("Нужно обучить wake word", "ok");
      return;
    }
    wakeEnabled = true;
    wakeButton.classList.add("active");
    wakeButton.textContent = "Выключить «Шо ты голова»";
    scheduleWakeRestart(0);
  } catch (error) {
    addMessage("Wake word", error.message, "error");
    setStatus(error.message, "error");
  } finally {
    wakeButton.disabled = false;
  }
}

function disableWakeWord() {
  wakeEnabled = false;
  clearTimeout(wakeRestartTimer);
  stopWakeListening();
  wakeButton.classList.remove("active");
  wakeButton.textContent = "Включить «Шо ты голова»";
  checkHealth();
}

async function checkHealth() {
  try {
    const response = await fetch("/api/health");
    const health = await response.json();
    pricingRates = health.pricing?.rates || null;
    statusText.dataset.model = health.model || "";
    if (health.ok) {
      setStatus(`Готов · ${health.model}`, "ok");
      return;
    }
    const missing = [];
    if (!health.openAI?.configured) missing.push("OPENAI_API_KEY");
    if (!health.homeAssistant?.authenticated) missing.push("HA_TOKEN");
    setStatus(`Нужна настройка: ${missing.join(", ") || "проверь Home Assistant"}`, "error");
  } catch {
    setStatus("Backend недоступен", "error");
  }
}

async function executeToolCall(event) {
  turnToolExecuted = true;
  let args;
  try {
    args = JSON.parse(event.arguments || "{}");
  } catch {
    args = {};
  }

  const command = `${args.action || "get_state"} → ${args.entity_id || "неизвестная сущность"}`;
  addMessage("Дом", `Выполняю: ${command}`);
  let output;
  try {
    const requestBody = { ...args, action: event.name === "get_home_state" ? "get_state" : args.action };
    const response = await fetch("/api/ha/entity", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(requestBody),
    });
    output = await response.json();
    if (!response.ok) throw new Error(output.error || "Команда отклонена");
    const resultText = `${output.name || output.entityId}: ${output.state || "готово"}`;
    addMessage("Home Assistant", resultText);
  } catch (error) {
    output = { ok: false, error: error.message };
    addMessage("Ошибка", error.message, "error");
  }

  dataChannel.send(
    JSON.stringify({
      type: "conversation.item.create",
      item: {
        type: "function_call_output",
        call_id: event.call_id,
        output: JSON.stringify(output),
      },
    }),
  );
  dataChannel.send(JSON.stringify({ type: "response.create" }));
}

function handleRealtimeEvent(event) {
  if (
    event.type === "response.function_call_arguments.done" &&
    ["control_home_entity", "get_home_state"].includes(event.name)
  ) {
    executeToolCall(event);
  }

  if (event.type === "input_audio_buffer.speech_started") {
    beginRequestUsage();
    setStatus("Слушаю…", "ok");
    connectButton.classList.add("speaking");
    scheduleSessionTimeout(15_000);
  }
  if (event.type === "input_audio_buffer.speech_stopped") {
    setStatus("Думаю…", "ok");
    connectButton.classList.remove("speaking");
  }
  if (event.type === "output_audio_buffer.started" || event.type === "response.output_audio.delta") {
    setStatus("Отвечаю…", "ok");
  }
  if (event.type === "response.output_text.delta") {
    setStatus("Пишу…", "ok");
  }
  if (event.type === "output_audio_buffer.stopped") {
    setStatus(turnToolExecuted ? "Готово" : "Жду уточнение…", "ok");
    scheduleSessionTimeout(turnToolExecuted ? 900 : 15_000);
  }
  if (event.type === "response.output_audio.done") {
    scheduleSessionTimeout(turnToolExecuted ? 2_500 : 15_000);
  }
  if (event.type === "response.done") {
    const hasFunctionCall = event.response?.output?.some((item) => item.type === "function_call");
    const responseText = getResponseText(event.response);
    if (responseText && !hasFunctionCall) addMessage("Ассистент", responseText);
    recordResponseUsage(event);
    if (hasFunctionCall) {
      clearTimeout(wakeSessionTimer);
    } else if (turnToolExecuted) {
      scheduleSessionTimeout(sessionResponseMode === "text" ? 500 : 5_000);
    } else {
      setStatus("Жду уточнение…", "ok");
      scheduleSessionTimeout(15_000);
    }
  }
  if (event.type === "conversation.item.input_audio_transcription.completed" && event.transcript) {
    addMessage("Вы", event.transcript);
  }
  if (event.type === "response.output_audio_transcript.done" && event.transcript) {
    addMessage("Ассистент", event.transcript);
  }
  if (event.type === "error") {
    const message = event.error?.message || "Ошибка Realtime API";
    addMessage("OpenAI", message, "error");
    setStatus(message, "error");
  }
}

async function startSession() {
  if (connecting || peerConnection) return;
  connecting = true;
  sessionResponseMode = responseModeSelect.value;
  connectButton.disabled = true;
  responseModeSelect.disabled = true;
  buttonLabel.textContent = "Подключаю…";
  setStatus("Запрашиваю микрофон…");
  if (wakeListening) await stopWakeListening();

  try {
    const pc = new RTCPeerConnection();
    peerConnection = pc;
    remoteAudio = new Audio();
    remoteAudio.autoplay = true;
    pc.ontrack = (event) => {
      remoteAudio.srcObject = event.streams[0];
    };

    microphoneStream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
    pc.addTrack(microphoneStream.getAudioTracks()[0], microphoneStream);

    dataChannel = pc.createDataChannel("oai-events");
    dataChannel.addEventListener("message", (message) => {
      try {
        handleRealtimeEvent(JSON.parse(message.data));
      } catch (error) {
        console.error("Invalid Realtime event", error);
      }
    });
    dataChannel.addEventListener("open", () => {
      setStatus("Слушаю…", "ok");
      connectButton.classList.add("connected");
      buttonLabel.textContent = "Завершить";
      muteButton.disabled = false;
      addMessage("Система", "Голосовая сессия запущена.");
      scheduleSessionTimeout(15_000);
    });

    pc.addEventListener("connectionstatechange", () => {
      if (["failed", "disconnected", "closed"].includes(pc.connectionState) && peerConnection) {
        setStatus(`Соединение: ${pc.connectionState}`, pc.connectionState === "failed" ? "error" : "");
      }
    });

    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    const response = await fetch(`/session?response_mode=${encodeURIComponent(sessionResponseMode)}`, {
      method: "POST",
      headers: { "Content-Type": "application/sdp" },
      body: offer.sdp,
    });
    if (!response.ok) {
      const error = await response.json().catch(() => ({}));
      throw new Error(error.details || error.error || `Session error ${response.status}`);
    }

    await pc.setRemoteDescription({ type: "answer", sdp: await response.text() });
  } catch (error) {
    addMessage("Ошибка", error.message, "error");
    setStatus(error.message, "error");
    stopSession(false);
  } finally {
    connecting = false;
    connectButton.disabled = false;
  }
}

function stopSession(showMessage = true) {
  clearTimeout(wakeSessionTimer);
  microphoneStream?.getTracks().forEach((track) => track.stop());
  dataChannel?.close();
  peerConnection?.close();
  if (remoteAudio) remoteAudio.srcObject = null;
  microphoneStream = undefined;
  dataChannel = undefined;
  peerConnection = undefined;
  remoteAudio = undefined;
  muted = false;
  muteButton.disabled = true;
  responseModeSelect.disabled = false;
  muteButton.textContent = "Выключить микрофон";
  connectButton.classList.remove("connected", "speaking");
  buttonLabel.textContent = "Начать";
  if (showMessage) addMessage("Система", "Сессия завершена.");
  wakeSession = false;
  if (wakeEnabled) scheduleWakeRestart(700);
  else checkHealth();
}

connectButton.addEventListener("click", () => {
  if (peerConnection) stopSession();
  else {
    wakeSession = false;
    startSession();
  }
});

wakeButton.addEventListener("click", () => {
  if (wakeEnabled) disableWakeWord();
  else enableWakeWord();
});

recordWakeButton.addEventListener("click", () => collectWakeExample(WAKE_LABEL));
recordNoiseButton.addEventListener("click", () => collectWakeExample(NOISE_LABEL));
trainWakeButton.addEventListener("click", trainWakeModel);

muteButton.addEventListener("click", () => {
  muted = !muted;
  microphoneStream?.getAudioTracks().forEach((track) => {
    track.enabled = !muted;
  });
  muteButton.textContent = muted ? "Включить микрофон" : "Выключить микрофон";
  setStatus(muted ? "Микрофон выключен" : "Слушаю…", "ok");
});

clearButton.addEventListener("click", () => messages.replaceChildren());
window.addEventListener("beforeunload", () => {
  wakeEnabled = false;
  stopWakeListening();
  stopSession(false);
});

checkHealth();
