"use strict";

const route = window.location.pathname;
const message = document.querySelector("#message");
for (const page of document.querySelectorAll("[data-route]")) {
  page.hidden = page.dataset.route !== route;
}
for (const link of document.querySelectorAll("nav a")) {
  if (link.pathname === route) link.setAttribute("aria-current", "page");
}

async function loadDevice() {
  try {
    const response = await fetch("/api/v1/workbench");
    if (response.status === 401) {
      window.location.replace("/login");
      return;
    }
    if (!response.ok) throw new Error();
    const {hostname, firmware} = await response.json();
    if (hostname) document.querySelector("#hostname").textContent = hostname;
    document.querySelector("#firmware").textContent = firmware || "固件版本未知";
  } catch {
    message.textContent = "设备信息暂时不可用。";
  }
}

document.querySelector("#sign-out").addEventListener("click", async (event) => {
  const button = event.currentTarget;
  button.disabled = true;
  try {
    const response = await fetch("/api/v1/logout", {method: "POST"});
    if (!response.ok) throw new Error();
    window.location.replace("/login");
  } catch {
    message.textContent = "退出失败，请重试。";
    button.disabled = false;
  }
});

loadDevice();
