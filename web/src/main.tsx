// SPDX-License-Identifier: GPL-3.0-or-later

import React from "react";
import ReactDOM from "react-dom/client";
import { HashRouter } from "react-router-dom";
import App from "./App";
import { AuthGate } from "./Login";
import "./tokens.css";
import "./app.css";

// The browser's own restore fires before a remounted list has content.
if ("scrollRestoration" in history) history.scrollRestoration = "manual";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <HashRouter>
      <AuthGate>
        <App />
      </AuthGate>
    </HashRouter>
  </React.StrictMode>
);
