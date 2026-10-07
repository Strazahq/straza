import React from "react";
import ReactDOM from "react-dom/client";
import { SelfServiceApp } from "./self-service/app";
import { setHarness } from "./lib/session";
import "./index.css";

setHarness("self-service");

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <SelfServiceApp />
  </React.StrictMode>,
);
