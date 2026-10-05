import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Selector } from "./Selector";
import "./selector.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Selector />
  </StrictMode>,
);
