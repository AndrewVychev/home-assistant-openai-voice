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
const providerSelect = document.querySelector("#provider");

let liveSocket;
let microphoneStream;
let captureContext;
let captureSource;
let captureProcessor;
let captureSink;
let playbackContext;
let nextPlaybackTime = 0;
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
let sessionResponseMode = "text";
let sessionProvider = "gemini";
let turnToolExecuted = false;
let inputTranscript = "";
let outputTranscript = "";
let textResponse = "";
let lastUsage;

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

function formatTokens(value) {
  return new Intl.NumberFormat("ru-RU").format(value || 0);
}

function recordUsage(usage) {
  lastUsage = usage;
  const input = usage.promptTokenCount || usage.input_tokens || 0;
  const output = usage.responseTokenCount || usage.output_tokens || 0;
  const total = usage.totalTokenCount || usage.total_tokens || input + output;
  lastCost.textContent = formatTokens(input + output);
  totalCost.textContent = "см. billing";
  usageDetails.textContent = `вход ${formatTokens(input)} · выход ${formatTokens(output)} · всего ${formatTokens(total)}`;
  console.info("Live API usage", usage);
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
  if (!wakeEnabled || !wakeModelReady || liveSocket || connecting || wakeListening) return;
  wakeRestartTimer = setTimeout(async () => {
    if (!wakeEnabled || liveSocket || connecting || wakeListening) return;
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
    if (liveSocket) stopSession(true);
  }, delay);
}

async function triggerWakeWord() {
  if (wakeTriggered || liveSocket || connecting) return;
  wakeTriggered = true;
  wakeSession = true;
  await stopWakeListening();
  setStatus("Фраза услышана · подключаюсь…", "ok");
  addMessage("Система", "Услышал «Шо ты голова».");
  playWakeChime();
  await startSession();
  wakeTriggered = false;
  if (!liveSocket) {
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
    const selected = providerSelect.value;
    const provider = health.providers?.[selected];
    statusText.dataset.model = provider?.model || health.model || "";
    if (health.homeAssistant?.authenticated && provider?.configured) {
      setStatus(`Готов · ${selected} · ${provider.model}`, "ok");
      return;
    }
    const missing = [];
    if (!provider?.configured) missing.push(selected === "openai" ? "OPENAI_API_KEY" : "GEMINI_API_KEY");
    if (!health.homeAssistant?.authenticated) missing.push("HA_TOKEN");
    setStatus(`Нужна настройка: ${missing.join(", ") || "проверь Home Assistant"}`, "error");
  } catch {
    setStatus("Backend недоступен", "error");
  }
}

function mergeTranscript(current, next) {
  if (!next) return current;
  if (!current || next.startsWith(current)) return next;
  return `${current}${next}`;
}

function floatToPcm16Base64(samples, sourceRate) {
  const ratio = sourceRate / 16_000;
  const length = Math.floor(samples.length / ratio);
  const bytes = new Uint8Array(length * 2);
  const view = new DataView(bytes.buffer);
  for (let index = 0; index < length; index += 1) {
    const start = Math.floor(index * ratio);
    const end = Math.max(start + 1, Math.floor((index + 1) * ratio));
    let sum = 0;
    for (let sourceIndex = start; sourceIndex < end && sourceIndex < samples.length; sourceIndex += 1) {
      sum += samples[sourceIndex];
    }
    const sample = Math.max(-1, Math.min(1, sum / (end - start)));
    view.setInt16(index * 2, sample < 0 ? sample * 0x8000 : sample * 0x7fff, true);
  }
  let binary = "";
  for (let index = 0; index < bytes.length; index += 1) binary += String.fromCharCode(bytes[index]);
  return btoa(binary);
}

async function startAudioCapture() {
  const AudioContextApi = window.AudioContext || window.webkitAudioContext;
  captureContext = new AudioContextApi();
  await captureContext.resume();
  captureSource = captureContext.createMediaStreamSource(microphoneStream);
  captureProcessor = captureContext.createScriptProcessor(4096, 1, 1);
  captureSink = captureContext.createGain();
  captureSink.gain.value = 0;
  captureProcessor.onaudioprocess = (event) => {
    if (muted || liveSocket?.readyState !== WebSocket.OPEN) return;
    const data = floatToPcm16Base64(event.inputBuffer.getChannelData(0), captureContext.sampleRate);
    liveSocket.send(JSON.stringify({ type: "audio", data }));
  };
  captureSource.connect(captureProcessor);
  captureProcessor.connect(captureSink);
  captureSink.connect(captureContext.destination);
}

async function playPcmChunk(base64, mimeType) {
  const AudioContextApi = window.AudioContext || window.webkitAudioContext;
  playbackContext ||= new AudioContextApi();
  await playbackContext.resume();
  const rate = Number(/rate=(\d+)/.exec(mimeType || "")?.[1] || 24_000);
  const binary = atob(base64);
  const sampleCount = Math.floor(binary.length / 2);
  const buffer = playbackContext.createBuffer(1, sampleCount, rate);
  const channel = buffer.getChannelData(0);
  for (let index = 0; index < sampleCount; index += 1) {
    const low = binary.charCodeAt(index * 2);
    const high = binary.charCodeAt(index * 2 + 1);
    const value = (high << 8) | low;
    channel[index] = (value & 0x8000 ? value - 0x10000 : value) / 0x8000;
  }
  const source = playbackContext.createBufferSource();
  source.buffer = buffer;
  source.connect(playbackContext.destination);
  const startAt = Math.max(playbackContext.currentTime + 0.02, nextPlaybackTime);
  source.start(startAt);
  nextPlaybackTime = startAt + buffer.duration;
}

function flushTurnMessages() {
  if (inputTranscript.trim()) addMessage("Вы", inputTranscript.trim());
  const assistantText = (outputTranscript || textResponse).trim();
  if (assistantText) addMessage("Ассистент", assistantText);
  inputTranscript = "";
  outputTranscript = "";
  textResponse = "";
}

function handleLiveEvent(event) {
  if (event.type === "ready") {
    statusText.dataset.model = event.model;
    sessionProvider = event.provider || sessionProvider;
    setStatus("Слушаю…", "ok");
    connectButton.classList.add("connected");
    buttonLabel.textContent = "Завершить";
    muteButton.disabled = false;
    addMessage("Система", `${sessionProvider} Live-сессия запущена.`);
    startAudioCapture().catch((error) => {
      addMessage("Ошибка", error.message, "error");
      stopSession(false);
    });
    scheduleSessionTimeout(15_000);
  } else if (event.type === "input_transcript") {
    inputTranscript = mergeTranscript(inputTranscript, event.text);
    setStatus("Думаю…", "ok");
    connectButton.classList.add("speaking");
    scheduleSessionTimeout(15_000);
  } else if (event.type === "output_transcript") {
    outputTranscript = mergeTranscript(outputTranscript, event.text);
    setStatus("Отвечаю…", "ok");
    connectButton.classList.remove("speaking");
  } else if (event.type === "text_delta") {
    textResponse += event.text || "";
    setStatus("Пишу…", "ok");
  } else if (event.type === "audio_delta") {
    setStatus("Отвечаю…", "ok");
    playPcmChunk(event.data, event.mimeType).catch(console.error);
  } else if (event.type === "tool_call") {
    turnToolExecuted = true;
    textResponse = "";
    clearTimeout(wakeSessionTimer);
    const args = event.args || {};
    addMessage("Дом", `Выполняю: ${args.action || "get_state"} → ${args.entity_id || "неизвестная сущность"}`);
  } else if (event.type === "ha_result") {
    const result = event.result || {};
    if (result.ok) addMessage("Home Assistant", `${result.name || result.entityId}: ${result.state || "готово"}`);
    else addMessage("Ошибка", result.error || "Команда отклонена", "error");
  } else if (event.type === "usage") {
    recordUsage(event.usage || {});
  } else if (event.type === "interrupted") {
    nextPlaybackTime = playbackContext?.currentTime || 0;
    setStatus("Слушаю…", "ok");
  } else if (event.type === "turn_complete") {
    setTimeout(flushTurnMessages, 250);
    if (turnToolExecuted) {
      const playbackDelay = playbackContext
        ? Math.max(0, (nextPlaybackTime - playbackContext.currentTime) * 1000)
        : 0;
      setStatus("Готово", "ok");
      scheduleSessionTimeout(playbackDelay + 500);
    } else {
      setStatus("Жду уточнение…", "ok");
      scheduleSessionTimeout(15_000);
    }
  } else if (event.type === "error") {
    addMessage(sessionProvider, event.message || "Ошибка Live API", "error");
    setStatus(event.message || "Ошибка Live API", "error");
  }
}

async function startSession() {
  if (connecting || liveSocket) return;
  connecting = true;
  sessionResponseMode = responseModeSelect.value;
  sessionProvider = providerSelect.value;
  turnToolExecuted = false;
  inputTranscript = "";
  outputTranscript = "";
  textResponse = "";
  connectButton.disabled = true;
  responseModeSelect.disabled = true;
  providerSelect.disabled = true;
  buttonLabel.textContent = "Подключаю…";
  setStatus("Запрашиваю микрофон…");
  if (wakeListening) await stopWakeListening();

  try {
    microphoneStream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const query = new URLSearchParams({ response_mode: sessionResponseMode, provider: sessionProvider });
    const socket = new WebSocket(`${protocol}//${location.host}/live?${query}`);
    liveSocket = socket;
    socket.addEventListener("message", (message) => {
      if (liveSocket !== socket) return;
      try {
        handleLiveEvent(JSON.parse(message.data));
      } catch (error) {
        console.error("Invalid Live event", error);
      }
    });
    socket.addEventListener("error", () => {
      setStatus("Ошибка WebSocket Live API", "error");
    });
    socket.addEventListener("close", () => {
      if (liveSocket === socket) stopSession(false);
    });
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
  if (liveSocket?.readyState === WebSocket.OPEN) {
    liveSocket.send(JSON.stringify({ type: "audio_stream_end" }));
  }
  microphoneStream?.getTracks().forEach((track) => track.stop());
  captureProcessor?.disconnect();
  captureSource?.disconnect();
  captureSink?.disconnect();
  captureContext?.close();
  playbackContext?.close();
  const socket = liveSocket;
  liveSocket = undefined;
  socket?.close();
  microphoneStream = undefined;
  captureContext = undefined;
  captureSource = undefined;
  captureProcessor = undefined;
  captureSink = undefined;
  playbackContext = undefined;
  nextPlaybackTime = 0;
  muted = false;
  muteButton.disabled = true;
  responseModeSelect.disabled = false;
  providerSelect.disabled = false;
  muteButton.textContent = "Выключить микрофон";
  connectButton.classList.remove("connected", "speaking");
  buttonLabel.textContent = "Начать";
  if (showMessage) addMessage("Система", "Сессия завершена.");
  wakeSession = false;
  if (wakeEnabled) scheduleWakeRestart(700);
  else checkHealth();
}

connectButton.addEventListener("click", () => {
  if (liveSocket) stopSession();
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
providerSelect.addEventListener("change", checkHealth);
window.addEventListener("beforeunload", () => {
  wakeEnabled = false;
  stopWakeListening();
  stopSession(false);
});

checkHealth();
