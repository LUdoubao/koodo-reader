import React from "react";
import "./mobileSheetClose.css";

export interface MobileSheetCloseProps {
  /** Accessible label, already translated by the caller. */
  label: string;
  /** Existing close callback owned by the panel being dismissed. */
  onClose: () => void;
}

/**
 * Full-screen panel close button, rendered only in mobile layout. The panel
 * keeps its own open/close state, so this component only forwards the event.
 */
const MobileSheetClose: React.FC<MobileSheetCloseProps> = (props) => {
  return (
    <div
      className="mobile-sheet-close mobile-tap-target"
      role="button"
      aria-label={props.label}
      onClick={() => {
        props.onClose();
      }}
    >
      <span className="icon-close"></span>
    </div>
  );
};

export default MobileSheetClose;
