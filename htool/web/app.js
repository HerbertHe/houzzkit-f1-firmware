"use strict";

const field = (id) => document.getElementById(id);
const form = field("setup");
const rootPassword = field("root-password");
const username = field("username");
const password = field("password");
const message = field("message");
const buttons = [field("next"), field("finish")];
const usernamePattern = /^[a-z][a-z0-9_-]{2,31}$/;
const passwordPattern = /^[!-~]{12,72}$/;
let step = 1;

function showStep(target) {
  step = target;
  for (const panel of document.querySelectorAll("[data-panel]")) {
    panel.hidden = Number(panel.dataset.panel) !== target;
    for (const input of panel.querySelectorAll("input")) input.disabled = panel.hidden;
  }
  for (const item of document.querySelectorAll("[data-step]")) {
    const index = Number(item.dataset.step);
    if (index === target) item.setAttribute("aria-current", "step");
    else item.removeAttribute("aria-current");
    item.dataset.complete = String(index < target);
  }
}

function completed(name) {
  form.reset();
  message.textContent = "";
  showStep(3);
  form.hidden = true;
  field("done").hidden = false;
  const host = window.location.hostname.replace(/^\[|\]$/g, "");
  field("ssh-command").textContent = "ssh " + name + "@" + host;
}

async function status() {
  try {
    const response = await fetch("/api/v1/setup/status", {cache: "no-store"});
    const data = await response.json();
    if (!response.ok) throw new Error();
    if (data.state === "complete") {
      completed(data.username);
      return;
    }
    if (data.state === "running") {
      message.textContent = "设备正在设置，请稍候。";
      setTimeout(status, 1500);
      return;
    }
    if (data.state === "pending") {
      username.value = data.username;
      username.readOnly = true;
      message.textContent = "检测到上一次设置中断，请使用原用户名重新填写两组密码。";
    }
    buttons.forEach((button) => { button.disabled = false; });
  } catch {
    message.textContent = "暂时无法读取设备状态。请刷新页面重试。";
  }
}

field("back").addEventListener("click", () => {
  message.textContent = "";
  showStep(1);
  rootPassword.focus();
});

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  message.textContent = "";
  if (step === 1) {
    if (!passwordPattern.test(rootPassword.value) || rootPassword.value !== field("root-confirm").value) {
      message.textContent = "请使用 12–72 个可见 ASCII 字符，并确认两次管理员密码一致。";
      rootPassword.focus();
      return;
    }
    showStep(2);
    username.focus();
    return;
  }
  if (!usernamePattern.test(username.value) || !passwordPattern.test(password.value) ||
      password.value !== field("confirm").value || password.value === rootPassword.value) {
    message.textContent = "请检查用户名、密码确认，以及两组密码是否不同。";
    return;
  }
  buttons.forEach((button) => { button.disabled = true; });
  field("back").disabled = true;
  try {
    const response = await fetch("/api/v1/setup", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({username: username.value, password: password.value, rootPassword: rootPassword.value}),
    });
    const data = await response.json();
    if (data.state === "complete") {
      completed(data.username);
      return;
    }
    message.textContent = data.error || "设置还未完成，请重试。";
  } catch {
    message.textContent = "连接中断。请刷新页面查看设置结果。";
  } finally {
    buttons.forEach((button) => { button.disabled = false; });
    field("back").disabled = false;
  }
});

showStep(1);
status();
