const SAFE_ACTIONS = {
  light: new Set(["get_state", "turn_on", "turn_off", "toggle"]),
  switch: new Set(["get_state", "turn_on", "turn_off", "toggle"]),
  climate: new Set(["get_state", "turn_on", "turn_off", "set_temperature"]),
};

export function validateEntityAction({ entityId, action, temperature }) {
  if (typeof entityId !== "string" || !/^[a-z_]+\.[a-z0-9_]+$/.test(entityId)) {
    return { ok: false, reason: "Некорректный entity_id." };
  }

  const domain = entityId.split(".", 1)[0];
  if (!SAFE_ACTIONS[domain]?.has(action)) {
    return { ok: false, reason: `Действие ${action || "не задано"} запрещено для ${domain}.` };
  }

  if (action === "set_temperature") {
    const value = Number(temperature);
    if (!Number.isFinite(value) || value < 10 || value > 30) {
      return { ok: false, reason: "Допустимая температура: от 10 до 30 °C." };
    }
    return { ok: true, entityId, domain, action, temperature: value };
  }

  return { ok: true, entityId, domain, action };
}

export function actionStateMatches({ domain, action, temperature }, state, previousState) {
  if (!state?.state || ["unknown", "unavailable"].includes(state.state)) return false;
  if (action === "turn_on") {
    return domain === "climate" ? state.state !== "off" : state.state === "on";
  }
  if (action === "turn_off") return state.state === "off";
  if (action === "set_temperature") {
    return Math.abs(Number(state.attributes?.temperature) - temperature) < 0.1;
  }
  if (action === "toggle") {
    return previousState?.state === "on" ? state.state === "off" : state.state === "on";
  }
  return false;
}
