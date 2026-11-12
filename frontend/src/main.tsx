import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./styles.css";
import { UIErrorBoundary } from "./UIErrorBoundary";
import { startUIHeartbeat } from "./uiRecovery.js";

startUIHeartbeat();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <UIErrorBoundary scope="application"><App /></UIErrorBoundary>
  </React.StrictMode>,
);
