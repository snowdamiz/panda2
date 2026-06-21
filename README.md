# Panda Discord Assistant

Panda is a Discord bot. People can use slash commands, or they can talk to it by saying `Panda` in a server message.

This guide is for getting Panda online with Fly.io.

## What You Need

- A Discord account with permission to create a Discord app.
- A Fly.io account.
- An OpenRouter account with an API key.
- A terminal on your computer.

Fly.io and OpenRouter may charge money depending on usage. Keep your keys private.

## 1. Make The Discord App

1. Go to the Discord Developer Portal.
2. Create a new application.
3. Open the Bot page.
4. Copy the bot token. This becomes `DISCORD_BOT_TOKEN`.
5. Turn on these bot settings:
   - Server Members Intent
   - Message Content Intent
6. Open the General Information page.
7. Copy the Application ID. This becomes `DISCORD_APPLICATION_ID`.
8. Copy the Public Key. This becomes `DISCORD_PUBLIC_KEY`.
9. Copy your own Discord user ID. This becomes `OWNER_USER_IDS`.

To copy your Discord user ID, turn on Developer Mode in Discord, then right-click your user and choose Copy User ID.

## 2. Make The OpenRouter Key

1. Go to OpenRouter.
2. Create an API key.
3. Copy it. This becomes `OPENROUTER_API_KEY`.

## 3. Create The Fly App

Install Fly's command line tool if you do not already have it:

```bash
brew install flyctl
```

If that command does not work, use the Fly install guide linked at the bottom of this README.

Log in:

```bash
fly auth login
```

Pick one app name. Use lowercase letters, numbers, and dashes only.
Use that same name everywhere you see `my-panda-bot` below.

Example app name:

```text
my-panda-bot
```

Open `fly.toml` and change the app name to your app name:

```toml
app = "my-panda-bot"
```

Create the app:

```bash
fly apps create my-panda-bot
```

Create storage for Panda's database:

```bash
fly volumes create data --app my-panda-bot --region sjc --size 1
```

## 4. Add The Secrets

Replace each `paste_here` value, then run this command:

```bash
fly secrets set DISCORD_BOT_TOKEN="paste_here" DISCORD_APPLICATION_ID="paste_here" DISCORD_PUBLIC_KEY="paste_here" OPENROUTER_API_KEY="paste_here" OWNER_USER_IDS="paste_here" --app my-panda-bot
```

Optional: add Brave Search if you want Panda to search the web:

```bash
fly secrets set BRAVE_SEARCH_API_KEY="paste_here" --app my-panda-bot
```

## 5. Deploy

Run:

```bash
fly deploy --app my-panda-bot
```

The first deploy can take a few minutes.

## 6. Connect Discord To The Live Bot

Your bot's web address will be:

```text
https://my-panda-bot.fly.dev
```

In the Discord Developer Portal:

1. Open your application.
2. Open the Webhooks page.
3. Set the webhook events endpoint to:

```text
https://my-panda-bot.fly.dev/discord/webhook-events
```

4. Enable webhook events.
5. Subscribe to `APPLICATION_AUTHORIZED`.
6. Open the Installation page.
7. Use the install link to add Panda to your Discord server.

If Discord asks for scopes, Panda needs `bot` and `applications.commands`.

If Discord asks for bot permissions, start with View Channels, Send Messages, Read Message History, Connect, and Speak. Add Manage Roles only if you want Panda to assign roles.

After Panda is installed, it can answer in any channel where Discord permissions allow it. The user who installed Panda becomes the Panda owner for that server, and the Discord server owner always retains management access.

Admins can restrict Panda to chosen channels later:

```text
/admin channel action:allow channel:#panda
```

When at least one channel is allowed, regular assistant use is limited to allowed channels. Admins can still use admin commands elsewhere to fix access. Use the same Discord role in both `/admin role` profile commands when a server does not split admin and moderator roles.

## 7. Check It Worked

Check Fly:

```bash
fly status --app my-panda-bot
```

Watch logs:

```bash
fly logs --app my-panda-bot
```

In Discord, try:

```text
/ping
```

Then try:

```text
Panda hello
```

To try music, join a voice channel and say:

```text
Panda play one more time daft punk
```

Then use natural controls like `Panda pause`, `Panda resume`, `Panda skip`, `Panda queue`, or `Panda stop`.

Slash commands can take a few minutes to show up in Discord.

## Updating Panda Later

After changing the bot, deploy again:

```bash
fly deploy --app my-panda-bot
```

## Common Problems

If `fly` is not found, install Fly's command line tool and reopen your terminal.

If Panda joins the server but does not answer messages, make sure Message Content Intent is turned on in the Discord Bot page.

If Panda cannot see members or roles correctly, make sure Server Members Intent is turned on in the Discord Bot page.

If Panda says the server-side audio tools are not ready yet, try again after a moment. Panda provisions its own music sidecars in the data volume.

If deploy says the database path is missing or not writable, make sure the Fly volume was created with the name `data`.

If Panda says OpenRouter is missing, set `OPENROUTER_API_KEY` again.

## More Details

Use `OPERATIONS.md` for backups, rollbacks, health checks, and incident steps.

Helpful links:

- [Fly install guide](https://fly.io/docs/flyctl/install/)
- [Fly deploy command](https://fly.io/docs/flyctl/deploy/)
- [Fly secrets command](https://fly.io/docs/flyctl/secrets-set/)
- [Fly volumes command](https://fly.io/docs/flyctl/volumes-create/)
- [Discord privileged intents](https://support-dev.discord.com/hc/en-us/articles/6207308062871-What-are-Privileged-Intents)
- [OpenRouter authentication](https://openrouter.ai/docs/api/reference/authentication)
