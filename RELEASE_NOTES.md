# Larik v0.9.0

## Highlights

- Add local speech input and output through OpenAI-compatible STT and TTS endpoints.
- Add `/stt-language id|en` to switch between Indonesian and English transcription.
- Add an animated microphone indicator and transcription status to the TUI.
- Normalize JSON transcription responses to the returned `text` field.
- Fix `Ctrl+Space` so the configured `record_audio` binding reaches the recorder.
- Keep audio configuration personal-only because it uses the microphone, local audio processes, and configured endpoints.

## Validation

- `go test ./...`
- `go build -o larik ./cmd/larik`
- Website generation and link checks
- Release archives and SHA256 checksums
