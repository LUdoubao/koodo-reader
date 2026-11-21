import React from "react";
import "./mobileSheet.css";

export interface MobileSheetProps {
  /** Existing dialog class names; the sheet class is added alongside them. */
  className?: string;
  /** Accessible name, already translated by the caller. */
  label?: string;
  children: React.ReactNode;
}

/**
 * Presentational full-screen sheet shell for dialogs. It owns no state and no
 * business logic: callers keep their existing container class, visibility
 * rules and close handling. On desktop the sheet rules do not apply, so the
 * wrapper is transparent there.
 */
const MobileSheet: React.FC<MobileSheetProps> = (props) => {
  return (
    <div
      className={
        "mobile-sheet" + (props.className ? " " + props.className : "")
      }
      role="dialog"
      aria-modal="true"
      aria-label={props.label}
    >
      {props.children}
    </div>
  );
};

export default MobileSheet;
