-- =============================================================================
-- Remove o estado morto 'REJECTED' de pix_payments.
--
-- Ele existia no CHECK desde o início e nunca foi escrito: quando o SPI
-- responde RJCT, o que o nosso ledger registra é o ESTORNO (`pix_estorno`,
-- devolvendo a reserva à carteira do pagador) e o pagamento termina REFUNDED.
-- O motivo da rejeição continua guardado em `spi_status` e `spi_reason`.
--
-- Estado que nunca acontece é estado que mente: quem lê o schema imagina um
-- fluxo que não existe. Menos estado, menos mentira.
--
-- O 001_ledger.sql já nasce com os três estados; esta migration existe para os
-- bancos que aplicaram a versão anterior. É segura nos dois casos.
-- =============================================================================

ALTER TABLE pix_payments DROP CONSTRAINT IF EXISTS pix_payments_status_check;

ALTER TABLE pix_payments ADD CONSTRAINT pix_payments_status_check
    CHECK (status IN ('RESERVED', 'SETTLED', 'REFUNDED'));
