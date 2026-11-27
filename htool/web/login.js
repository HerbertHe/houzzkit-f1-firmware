"use strict";

const form = document.querySelector("#login");
const password = document.querySelector("#admin-password");
const button = form.querySelector("button");
const message = document.querySelector("#message");

button.disabled = false;
form.addEventListener("submit", async (event) => {
  event.preventDefault();
  button.disabled = true;
  message.textContent = "";
  try {
    const response = await fetch("/api/v1/login", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({password: password.value}),
    });
    if (response.ok) {
      window.location.replace("/");
      return;
    }
    const result = await response.json();
    message.textContent = result.error || "登录失败。";
    password.focus();
  } catch {
    message.textContent = "无法连接设备，请重试。";
  } finally {
    password.value = "";
    button.disabled = false;
  }
});
