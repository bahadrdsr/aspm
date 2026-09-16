import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { Preferences } from "./lib/preferences";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("The application root is missing.");
createRoot(root).render(<StrictMode><Preferences><App /></Preferences></StrictMode>);
