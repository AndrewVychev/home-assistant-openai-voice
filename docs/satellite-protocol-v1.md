# Home Voice Satellite Protocol v1

Один протокол используется Android, Go-сателлитом и будущим ESP32-S3/ReSpeaker. Облачные ключи и токен Home Assistant никогда не передаются сателлиту.

## Соединение

```text
GET ws(s)://gateway:3000/live
    ?protocol=1
    &transport=binary
    &provider=openai|gemini
    &response_mode=audio|text
    &satellite_id=living-room
Authorization: Bearer <SATELLITE_TOKEN>
```

`SATELLITE_TOKEN` на gateway опционален только для локальной разработки. Для постоянной установки он обязателен. `satellite_id` стабилен и задаёт область краткосрочного контекста: контекст одной комнаты не протекает в другую.

## Аудио

- Satellite → Gateway: WebSocket binary frame, PCM signed 16-bit little-endian, mono, 16 kHz.
- Gateway → Satellite: WebSocket binary frame, PCM signed 16-bit little-endian, mono, 24 kHz.
- Рекомендуемый входной frame: 20 ms = 640 bytes.
- Микрофон выключается на время воспроизведения ответа, поэтому протокол сейчас half-duplex и не требует полноценного AEC.

После подключения gateway присылает текстовый JSON:

```json
{
  "type": "ready",
  "protocolVersion": 1,
  "transport": "binary",
  "provider": "openai",
  "model": "gpt-realtime-2.1-mini",
  "responseMode": "audio",
  "inputAudio": { "encoding": "pcm_s16le", "sampleRate": 16000, "channels": 1 },
  "outputAudio": { "encoding": "pcm_s16le", "sampleRate": 24000, "channels": 1 }
}
```

До `ready` аудио не отправляется. Управляющие и диагностические события остаются JSON text frames: `input_transcript`, `output_transcript`, `tool_call`, `ha_result`, `turn_complete`, `error`. Клиент может отправить `audio_stream_end` или `text`.

Старый `json-base64` транспорт временно поддерживается Go CLI для обратной совместимости.

## Жизненный цикл

1. Сателлит постоянно слушает только локальный activation engine или ждёт нажатия кнопки.
2. После активации он открывает `/live` и хранит короткий PCM pre-roll, чтобы не потерять начало команды.
3. После `ready` отправляет pre-roll и живой PCM.
4. Gateway подключает выбранный Live provider, исполняет разрешённый tool call и возвращает ответ.
5. На первом ответном аудио сателлит глушит микрофон и воспроизводит PCM.
6. После `turn_complete` закрывает облачную сессию либо продолжает её, если модель задала уточняющий вопрос.

## Совместимость

Новые несовместимые форматы получают новый `protocol` query parameter. Неизвестную версию gateway должен отклонять до открытия облачной сессии. Возможности конкретного железа в дальнейшем объявляются отдельным `hello` event; формат аудио v1 при этом не меняется.
