#ifndef INCLUDE_MDB_C
#define INCLUDE_MDB_C
#include <inttypes.h>
#include <stdbool.h>

/*
MDB timings:
  t = 1.0 mS inter-byte (max.)
  t = 5.0 mS response (max.)
  t = 100 mS break (min.)
  t = 200 mS setup (min.)
*/

static MDB_State_t volatile mdb_state = MDB_State_Idle;
static Buffer_t volatile mdb_in;
static uint8_t volatile mdb_in_data[MDB_PACKET_SIZE];  // data from MDB
static uint8_t volatile mdb_in_checksum;  // calculated checksum of mdb_in
static Buffer_t volatile mdb_out;
static uint8_t volatile mdb_out_data[MDB_PACKET_SIZE];  // data to MDB
static bool volatile mdb_state_retrying;
static Response_t volatile mdb_fast_error_code;
static MDB_State_t volatile mdb_fast_error_state;
static uint8_t volatile mdb_fast_error_data;

static void mdb_finish_2(Response_t const code, uint8_t const data);
static void mdb_finish_n(Response_t const code, uint8_t const* const data,
                         uint8_t const length);
static void mdb_start_receive(void);

static bool uart_send_ready(void) { return bit_mask_test(UCSR0A, _BV(UDRE0)); }

// Baud Rate = 9600 +1%/-2% NRZ 9-N-1
static void mdb_init(void) {
  Buffer_Init(&mdb_in, (uint8_t * const)mdb_in_data, sizeof(mdb_in_data));
  Buffer_Init(&mdb_out, (uint8_t * const)mdb_out_data, sizeof(mdb_out_data));
  mdb_reset();

  uint32_t const MDB_BAUDRATE = 9600;
  uint32_t const BAUD_PRESCALE = (((F_CPU / (MDB_BAUDRATE * 16UL))) - 1);
  // set baud rate
  UBRR0H = (BAUD_PRESCALE >> 8);
  UBRR0L = BAUD_PRESCALE;
  // UCSZ00+UCSZ01+UCSZ02 = 9 data bits
  UCSR0C = _BV(UCSZ00) | _BV(UCSZ01);
  UCSR0B = _BV(RXEN0) | _BV(TXEN0)  // Enable receiver and transmitter
           | _BV(UCSZ02)            // 9 data bit
           | _BV(RXCIE0)            // Enable RX Complete Interrupt
      ;
}

static void mdb_start_receive(void) {
  Buffer_Clear_Fast(&mdb_in);
  mdb_in_checksum = 0;
  mdb_state = MDB_State_Recv;
  timer0_set(MDB_TIMEOUT);
}

static void mdb_reset(void) {
  timer0_stop();
  mdb_fast_error_code = 0;
  mdb_fast_error_state = 0;
  mdb_fast_error_data = 0;
  Buffer_Clear_Fast(&mdb_in);
  Buffer_Clear_Fast(&mdb_out);
  mdb_in_checksum = 0;
  mdb_state_retrying = false;
  mdb_state = MDB_State_Idle;
}

static void mdb_start_send(void) {
  // TODO wait for byte is sent?
  if (!uart_send_ready()) {
    mdb_finish_2(Response_UART_Send_Busy, 0);
    return;
  }
  timer0_set(MDB_TIMEOUT);
  mdb_state = MDB_State_Send;
  mdb_state_retrying = false;

  uint8_t const csb = _BV(RXEN0) | _BV(TXEN0) | _BV(UCSZ02) | _BV(RXCIE0);
  UCSR0B = csb | _BV(TXB80);
  UDR0 = mdb_out.data[0];

  // clear 9 bit for following bytes
  UCSR0B = csb;

  // important to set index before UDRIE
  mdb_out.used = 1;
  UCSR0B = csb | _BV(UDRIE0);
  return;
}

static void mdb_fast_error(Response_t const code, uint8_t const data) {
  mdb_fast_error_state = mdb_state;
  mdb_state = MDB_State_Error;
  mdb_fast_error_code = code;
  mdb_fast_error_data = data;
}

static void mdb_finish_1(Response_t const code) {
  master_out_1(code);
  mdb_reset();
}
static void mdb_finish_2(Response_t const code, uint8_t const data) {
  master_out_2(code, data);
  mdb_reset();
}
static void mdb_finish_3(Response_t const code, uint8_t const data1,
                         uint8_t const data2) {
  uint8_t const buf[] = {data1, data2};
  master_out_n(code, buf, sizeof(buf));
  mdb_reset();
}
static void mdb_finish_n(Response_t const code, uint8_t const* const data,
                         uint8_t const length) {
  master_out_n(code, data, length);
  mdb_reset();
}

static bool mdb_step(void) {
  if (mdb_fast_error_code != 0) {
    mdb_finish_3(mdb_fast_error_code, mdb_fast_error_state,
                 mdb_fast_error_data);
    return true;
  }

  if (mdb_state == MDB_State_Recv_End) {
    uint8_t const len = mdb_in.length;
    if (len == 0) {
      mdb_finish_2(Response_MDB_Code_Error, __LINE__);
      return true;
    }
    uint8_t const last_byte = mdb_in.data[len - 1];
    if (len == 1) {
      // VMC ---ADD*---DAT---DAT---CHK-----
      // VMC ---ADD*---CHK--
      // Per -------------ACK*-
      // Per -------------NAK*-
      if (last_byte == MDB_ACK) {
        mdb_finish_1(Response_MDB_Success);
      } else if (last_byte == MDB_NAK) {
        mdb_finish_2(Response_MDB_NAK, last_byte);
      } else {
        mdb_finish_2(Response_MDB_Invalid_End, last_byte);
      }
      return true;
    }

    if (last_byte != mdb_in_checksum) {
      if (mdb_state_retrying) {
        // invalid checksum even after retry
        // VMC ---ADD*---DAT--CHK----------------RET----------------NAK--
        // Per ------------------DAT---DAT---CHK*---DAT---DAT---CHK*-----
        UDR0 = MDB_NAK;
        mdb_finish_n(Response_MDB_Invalid_CHK, mdb_in.data, len);
        return true;
      } else {
        // VMC ---ADD*---DAT--CHK----------------RET----------------ACK--
        // Per ------------------DAT---DAT---CHK*---DAT---DAT---CHK*-----
        UDR0 = MDB_RET;
        mdb_state_retrying = true;
        mdb_start_receive();
        return true;
      }
    }

    // VMC ---ADD*---CHK----------------ACK-
    // Per -------------DAT---DAT---CHK*----
    UDR0 = MDB_ACK;
    mdb_finish_n(Response_MDB_Success, mdb_in.data, len - 1);
    return true;
  }
  return false;
}

ISR(USART_RX_vect) {
  timer0_stop();
  // both UCSR[AB] must be read before UDR
  uint8_t const csa = UCSR0A;
  uint8_t const csb = UCSR0B;
  uint8_t const data = UDR0;

  // receive error
  // bit_mask_test would be true only if all error types happened at once
  //
  uint8_t const err = csa & (_BV(FE0) | _BV(DOR0) | _BV(UPE0));
  if (err != 0) {
    if (bit_mask_test(err, _BV(FE0))) {
      mdb_fast_error(Response_UART_Read_Error, err);
    } else if (bit_mask_test(err, _BV(DOR0))) {
      mdb_fast_error(Response_UART_Read_Overflow, err);
    } else if (bit_mask_test(err, _BV(UPE0))) {
      mdb_fast_error(Response_UART_Read_Parity, err);
    }
    return;
  }

  // received data out of session
  if (mdb_state != MDB_State_Recv) {
    mdb_fast_error(Response_UART_Read_Unexpected, data);
    return;
  }

  if (!Buffer_Append(&mdb_in, data)) {
    mdb_fast_error(Response_MDB_Receive_Overflow, data);
    return;
  }
  if (bit_mask_test(csb, _BV(RXB80))) {
    mdb_state = MDB_State_Recv_End;
  } else {
    mdb_in_checksum += data;
    timer0_set(MDB_TIMEOUT);
  }
}

// UART TX buffer space available
ISR(USART_UDRE_vect) {
  timer0_stop();
  // anti-volatile
  uint8_t const used = mdb_out.used;
  uint8_t const len = mdb_out.length;
  // debug mode
  if (used >= len) {
    mdb_fast_error(Response_MDB_Send_Overflow, used);
    return;
  }

  uint8_t const data = mdb_out.data[used];
  mdb_out.used++;

  // last byte is (about to be) sent
  if (mdb_out.used == len) {
    // variable hoop to make volatile UCSR0B assignment once
    uint8_t csb = UCSR0B;
    bit_mask_clear(csb, _BV(UDRIE0));  // disable (this) TX ready interrupt
    bit_mask_set(csb, _BV(TXCIE0));    // enable TX-finished interrupt
    UCSR0B = csb;
  }

  UDR0 = data;

  timer0_set(MDB_TIMEOUT);
}

// UART TX completed
ISR(USART_TX_vect) {
  timer0_stop();
  bit_mask_clear(UCSR0B, _BV(TXCIE0));  // disable (this) TX completed interrupt

  if (mdb_state != MDB_State_Send) {
    mdb_fast_error(Response_MDB_Code_Error, __LINE__);
    return;
  }

  mdb_start_receive();
}

#endif  // INCLUDE_MDB_C
