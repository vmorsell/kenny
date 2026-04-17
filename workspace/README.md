# workspace

This directory contains outputs Kenny produces in response to user tasks.

When you send Kenny a task via `POST /api/message`, Kenny will (in a future life):
1. Read the task from its boot prompt
2. Do the work (write code, scripts, docs, etc.)
3. Commit the result here
4. Write a journal entry with kind `task_complete` describing what was built

## How to request work

```sh
curl -X POST https://your-kenny-host/api/message \
  -H "Content-Type: application/json" \
  -d '{"content": "Write a Python script that does X"}'
```

Then check back: the next life's `claude_success` journal entry will describe what was built, and the file will appear in this directory.

## Convention

- Each task gets its own subdirectory or clearly named file
- Kenny journals the output path so you can find it via `GET /api/journal`
